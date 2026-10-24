package providers

import (
	"bufio"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SSEScanner reads an SSE (Server-Sent Events) stream line by line,
// extracting data payloads. Shared by OpenAI, Anthropic, and Codex providers.
type SSEScanner struct {
	reader    *bufio.Reader
	data      string
	eventType string
	err       error
	done      bool
}

// NewSSEScanner creates an SSE scanner.
//
// It reads lines with bufio.Reader.ReadString, which grows to fit a line of
// any length, instead of bufio.Scanner, whose fixed max-token size (formerly
// SSEScanBufMax) overflowed with "token too long" on a single data line
// carrying a full base64 image — image generation streams multi-MB frames in
// a single SSE line. SSEScanBufInit is reused as the reader's initial size.
func NewSSEScanner(r io.Reader) *SSEScanner {
	return &SSEScanner{reader: bufio.NewReaderSize(r, SSEScanBufInit)}
}

// Next advances to the next data line. Returns false when the stream ends
// or "[DONE]" is encountered. After Next returns false, call Err() for errors.
func (s *SSEScanner) Next() bool {
	if s.done {
		return false
	}
	for {
		line, err := s.reader.ReadString('\n')
		if len(line) > 0 {
			// Trim the trailing newline ("\n" or "\r\n").
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")

			if after, ok := strings.CutPrefix(line, "event: "); ok {
				s.eventType = after
			} else if after, ok := strings.CutPrefix(line, "event:"); ok {
				s.eventType = strings.TrimSpace(after)
			} else {
				payload, isData := "", false
				if after, ok := strings.CutPrefix(line, "data: "); ok {
					payload, isData = after, true
				} else if after, ok := strings.CutPrefix(line, "data:"); ok {
					payload, isData = after, true
				}
				if isData {
					// "[DONE]" is the OpenAI/Codex stream terminator.
					if payload == "[DONE]" {
						s.done = true
						return false
					}
					s.data = payload
					// A final line may arrive together with io.EOF (no trailing
					// newline). Deliver it now; the next call reports the end.
					if err != nil {
						s.done = true
						if err != io.EOF {
							s.err = err
						}
					}
					return true
				}
				// Non event/data line (blank line, ":" comment, other field): skip.
			}
		}
		if err != nil {
			if err != io.EOF {
				s.err = err
			}
			s.done = true
			return false
		}
	}
}

// Data returns the current data payload (valid after Next returns true).
func (s *SSEScanner) Data() string {
	return s.data
}

// EventType returns the last seen event type (e.g. "message_start", "content_block_delta").
func (s *SSEScanner) EventType() string {
	return s.eventType
}

// Err returns the first non-EOF error encountered during scanning.
func (s *SSEScanner) Err() error {
	return s.err
}

// DefaultStreamIdleTimeout bounds the gap BETWEEN bytes on an in-flight LLM
// stream. It is distinct from Transport.ResponseHeaderTimeout (which only bounds
// the wait for the FIRST byte): once a provider returns headers and starts
// streaming, nothing else bounds the read. A provider that returns headers and
// then stalls mid-stream (TCP alive, no further data) would otherwise wedge the
// read — and its scheduler lane — until OS TCP keepalive gives up (~2h), because
// agent run contexts come from the scheduler via context.WithCancel and carry no
// deadline.
//
// The idle timer resets on every chunk, so a legitimately long completion
// (extended thinking, large output) is never killed as long as it keeps making
// progress. Only a genuine stall trips it. Sized well above the longest plausible
// inter-chunk gap for a reasoning model that emits nothing while thinking.
const DefaultStreamIdleTimeout = 300 * time.Second

// StreamIdleTimeout returns the configured inter-chunk idle timeout, overridable
// via GOCLAW_LLM_STREAM_IDLE_TIMEOUT (seconds). A value of 0 disables the idle
// watchdog entirely (not recommended — restores the unbounded-stall behavior).
func StreamIdleTimeout() time.Duration {
	if v := os.Getenv("GOCLAW_LLM_STREAM_IDLE_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return DefaultStreamIdleTimeout
}

// CtxBody wraps an http.Response.Body so that ctx cancellation closes the
// underlying socket, unblocking a goroutine stuck inside a blocking read.
// Optionally it also closes the socket after an inter-read idle timeout, so a
// provider that stalls mid-stream cannot wedge the read indefinitely.
// Safe for concurrent Close (sync.Once). Caller MUST defer Close() to release
// the watchdog goroutine even on success.
type CtxBody struct {
	body         io.ReadCloser
	done         chan struct{}
	once         sync.Once
	idleTimeout  time.Duration
	activity     chan struct{} // non-blocking pokes on every read; nil when idle disabled
	idleTimedOut atomic.Bool
}

// NewCtxBody returns a ReadCloser that closes body when ctx is cancelled.
func NewCtxBody(ctx context.Context, body io.ReadCloser) *CtxBody {
	return NewCtxBodyIdle(ctx, body, 0)
}

// NewCtxBodyIdle returns a ReadCloser that closes body when ctx is cancelled OR
// when no Read makes progress within idleTimeout. idleTimeout <= 0 disables the
// idle watchdog (behaves like NewCtxBody).
func NewCtxBodyIdle(ctx context.Context, body io.ReadCloser, idleTimeout time.Duration) *CtxBody {
	cb := &CtxBody{body: body, done: make(chan struct{}), idleTimeout: idleTimeout}
	if idleTimeout > 0 {
		cb.activity = make(chan struct{}, 1)
	}
	go cb.watch(ctx)
	return cb
}

// watch closes the body on ctx cancellation, normal close, or (when enabled) an
// inter-read idle timeout. The idle timer resets on every read that reports
// activity, so only a genuine stall trips it.
func (cb *CtxBody) watch(ctx context.Context) {
	if cb.idleTimeout <= 0 {
		select {
		case <-ctx.Done():
			cb.closeOnce()
		case <-cb.done:
		}
		return
	}

	timer := time.NewTimer(cb.idleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			cb.closeOnce()
			return
		case <-cb.done:
			return
		case <-cb.activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(cb.idleTimeout)
		case <-timer.C:
			// No read progress within idleTimeout: treat as a stalled stream.
			cb.idleTimedOut.Store(true)
			cb.closeOnce()
			return
		}
	}
}

func (cb *CtxBody) Read(p []byte) (int, error) {
	n, err := cb.body.Read(p)
	// Poke the watchdog on any progress so the idle timer resets. Non-blocking:
	// a full channel already means "activity pending", so dropping is fine.
	if cb.activity != nil && (n > 0 || err == nil) {
		select {
		case cb.activity <- struct{}{}:
		default:
		}
	}
	return n, err
}

// IdleTimedOut reports whether the body was closed because of an inter-read idle
// timeout (as opposed to ctx cancellation or normal completion). Lets callers
// surface a clear, retryable stall error instead of a generic closed-socket read.
func (cb *CtxBody) IdleTimedOut() bool { return cb.idleTimedOut.Load() }

// Close closes the underlying body exactly once (safe for concurrent calls).
func (cb *CtxBody) Close() error {
	return cb.closeOnce()
}

func (cb *CtxBody) closeOnce() error {
	var err error
	cb.once.Do(func() {
		close(cb.done)
		err = cb.body.Close()
	})
	return err
}
