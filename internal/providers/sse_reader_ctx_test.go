package providers

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingCloser wraps an io.ReadCloser and counts Close calls.
type countingCloser struct {
	mu         sync.Mutex
	closeCount int
	reader     io.Reader
}

func newCountingCloser(r io.Reader) *countingCloser {
	return &countingCloser{reader: r}
}

func (c *countingCloser) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *countingCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCount++
	return nil
}

func (c *countingCloser) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCount
}

// blockingReader blocks Read until Close is called (simulates a live TCP socket).
type blockingReader struct {
	ch     chan struct{}
	closed atomic.Bool
}

func newBlockingReader() *blockingReader {
	return &blockingReader{ch: make(chan struct{})}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.ch
	return 0, errors.New("connection closed")
}

func (b *blockingReader) Close() error {
	if b.closed.CompareAndSwap(false, true) {
		close(b.ch)
	}
	return nil
}

// TestCtxBody_CancelClosesBody verifies that ctx cancellation calls body.Close exactly once
// and that a blocked Read returns an error.
func TestCtxBody_CancelClosesBody(t *testing.T) {
	t.Parallel()

	br := newBlockingReader()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cb := NewCtxBody(ctx, br)

	// Cancel context after a short delay.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	// Read should unblock when Close is called by the watchdog.
	buf := make([]byte, 8)
	_, err := cb.Read(buf)
	if err == nil {
		t.Fatal("expected error from Read after ctx cancel, got nil")
	}

	// Close should be a no-op (already closed by watchdog).
	if err2 := cb.Close(); err2 != nil {
		t.Fatalf("second Close returned error: %v", err2)
	}

	// Underlying body should be closed exactly once.
	if !br.closed.Load() {
		t.Fatal("expected underlying body to be closed")
	}
}

// TestCtxBody_NormalClose_WatchdogExits verifies that calling Close() directly
// releases the watchdog goroutine (no goroutine leak).
func TestCtxBody_NormalClose_WatchdogExits(t *testing.T) {
	t.Parallel()

	before := runtime.NumGoroutine()

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cc := newCountingCloser(io.NopCloser(io.LimitReader(nopReader{}, 0)))
			cb := NewCtxBody(ctx, cc)
			// Normal close — watchdog should exit via done channel.
			if err := cb.Close(); err != nil {
				t.Errorf("Close returned error: %v", err)
			}
		}()
	}
	wg.Wait()

	// Give goroutines time to exit.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	// Allow a small tolerance for unrelated goroutines.
	if after > before+5 {
		t.Errorf("goroutine leak: before=%d after=%d (delta=%d)", before, after, after-before)
	}
}

// TestCtxBody_ConcurrentCloseAndCancel verifies no double-close panic under
// concurrent Close() and ctx cancellation. Run with -race flag.
func TestCtxBody_ConcurrentCloseAndCancel(t *testing.T) {
	t.Parallel()

	const iterations = 500
	for range iterations {
		cc := newCountingCloser(io.NopCloser(io.LimitReader(nopReader{}, 0)))
		ctx, cancel := context.WithCancel(context.Background())

		cb := NewCtxBody(ctx, cc)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = cb.Close()
		}()
		go func() {
			defer wg.Done()
			cancel()
		}()
		wg.Wait()

		// Body must be closed exactly once regardless of race.
		if cnt := cc.Count(); cnt != 1 {
			t.Fatalf("expected close count=1, got %d", cnt)
		}
	}
}

// nopReader is an io.Reader that always returns (0, io.EOF).
type nopReader struct{}

func (nopReader) Read([]byte) (int, error) { return 0, io.EOF }

// TestCtxBody_IdleTimeout_TripsOnStall verifies that a read which blocks past
// the idle timeout causes the watchdog to close the body and mark IdleTimedOut.
func TestCtxBody_IdleTimeout_TripsOnStall(t *testing.T) {
	t.Parallel()

	br := newBlockingReader()
	ctx := context.Background() // no cancellation — only the idle timer can trip

	cb := NewCtxBodyIdle(ctx, br, 30*time.Millisecond)
	defer cb.Close()

	buf := make([]byte, 8)
	done := make(chan error, 1)
	go func() {
		_, err := cb.Read(buf)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from Read after idle timeout, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not unblock after idle timeout — watchdog failed to close socket")
	}

	if !cb.IdleTimedOut() {
		t.Fatal("expected IdleTimedOut() == true after stall")
	}
	if !br.closed.Load() {
		t.Fatal("expected underlying body to be closed by idle watchdog")
	}
}

// activityReader returns one byte per Read up to n times with a delay between
// each, then blocks forever. Models a stream making steady progress.
type activityReader struct {
	remaining atomic.Int32
	delay     time.Duration
	block     chan struct{}
}

func newActivityReader(chunks int, delay time.Duration) *activityReader {
	r := &activityReader{delay: delay, block: make(chan struct{})}
	r.remaining.Store(int32(chunks))
	return r
}

func (r *activityReader) Read(p []byte) (int, error) {
	if r.remaining.Add(-1) >= 0 {
		time.Sleep(r.delay)
		p[0] = 'x'
		return 1, nil
	}
	return 0, io.EOF // clean end of stream after steady progress
}

func (r *activityReader) Close() error {
	select {
	case <-r.block:
	default:
		close(r.block)
	}
	return nil
}

// TestCtxBody_IdleTimeout_ResetsOnActivity verifies the idle timer resets on
// every read that makes progress: a stream whose inter-chunk gap stays under
// the idle timeout is never killed, even if total duration exceeds it.
func TestCtxBody_IdleTimeout_ResetsOnActivity(t *testing.T) {
	t.Parallel()

	// 10 chunks, 15ms apart = 150ms total, well past the 40ms idle timeout —
	// but each individual gap (15ms) is under it, so the timer must keep resetting.
	ar := newActivityReader(10, 15*time.Millisecond)
	ctx := context.Background()
	cb := NewCtxBodyIdle(ctx, ar, 40*time.Millisecond)
	defer cb.Close()

	buf := make([]byte, 8)
	reads := 0
	for {
		n, err := cb.Read(buf)
		if n > 0 {
			reads++
		}
		if err != nil {
			break
		}
	}

	if cb.IdleTimedOut() {
		t.Fatalf("idle timeout tripped despite steady activity (got %d reads before stall)", reads)
	}
	if reads < 10 {
		t.Fatalf("expected at least 10 progressing reads before stall, got %d", reads)
	}
}

// TestStreamIdleTimeout_EnvOverride verifies the env override and default.
func TestStreamIdleTimeout_EnvOverride(t *testing.T) {
	t.Setenv("GOCLAW_LLM_STREAM_IDLE_TIMEOUT", "45")
	if got := StreamIdleTimeout(); got != 45*time.Second {
		t.Errorf("StreamIdleTimeout() = %v, want 45s", got)
	}

	t.Setenv("GOCLAW_LLM_STREAM_IDLE_TIMEOUT", "")
	if got := StreamIdleTimeout(); got != DefaultStreamIdleTimeout {
		t.Errorf("StreamIdleTimeout() = %v, want default %v", got, DefaultStreamIdleTimeout)
	}

	t.Setenv("GOCLAW_LLM_STREAM_IDLE_TIMEOUT", "0")
	if got := StreamIdleTimeout(); got != 0 {
		t.Errorf("StreamIdleTimeout() = %v, want 0 (disabled)", got)
	}
}
