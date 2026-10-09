package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// DocumentMaxBytes is the largest document read_document will analyze. Tool
// producers (e.g. MCP embedded documents) use the same cap so that anything
// they store is readable.
const DocumentMaxBytes = documentMaxBytes

// pdfMagic is the signature every PDF file starts with.
var pdfMagic = []byte("%PDF-")

// IsPDF reports whether data starts with the PDF signature.
func IsPDF(data []byte) bool {
	return bytes.HasPrefix(data, pdfMagic)
}

// --- Run-scoped document registry ---

const ctxRunDocuments toolContextKey = "tool_run_documents"

// RunDocuments collects document MediaRefs that tools produce during a run
// (as opposed to documents the user attached, which arrive via
// WithMediaDocRefs before the run starts). read_document consults it so a
// document produced by one tool call can be read by a later call in the same
// run. Thread-safe: tools may execute in parallel goroutines.
type RunDocuments struct {
	mu   sync.Mutex
	refs []providers.MediaRef
}

// NewRunDocuments creates an empty run document registry.
func NewRunDocuments() *RunDocuments {
	return &RunDocuments{}
}

// Add registers a document ref produced during the run.
func (rd *RunDocuments) Add(ref providers.MediaRef) {
	rd.mu.Lock()
	rd.refs = append(rd.refs, ref)
	rd.mu.Unlock()
}

// Refs returns a copy of the registered refs in insertion order.
func (rd *RunDocuments) Refs() []providers.MediaRef {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	return append([]providers.MediaRef(nil), rd.refs...)
}

// WithRunDocuments installs a run document registry in context.
func WithRunDocuments(ctx context.Context, rd *RunDocuments) context.Context {
	return context.WithValue(ctx, ctxRunDocuments, rd)
}

// RunDocumentsFromCtx returns the run document registry, or nil.
func RunDocumentsFromCtx(ctx context.Context) *RunDocuments {
	v, _ := ctx.Value(ctxRunDocuments).(*RunDocuments)
	return v
}

// availableDocumentRefs returns the conversation's document refs followed by
// documents produced earlier in this run, so the last element is the newest.
func availableDocumentRefs(ctx context.Context) []providers.MediaRef {
	refs := MediaDocRefsFromCtx(ctx)
	rd := RunDocumentsFromCtx(ctx)
	if rd == nil {
		return refs
	}
	produced := rd.Refs()
	if len(produced) == 0 {
		return refs
	}
	out := make([]providers.MediaRef, 0, len(refs)+len(produced))
	out = append(out, refs...)
	return append(out, produced...)
}

// --- Persisting tool-produced documents ---

// StoredDocument describes a document persisted by PersistToolDocument.
type StoredDocument struct {
	Ref         providers.MediaRef
	Name        string // sanitized display name, including extension
	LogicalPath string // path relative to the workspace, usable as read_document(path=...)
	Size        int
}

// maxDocumentStemRunes caps the sanitized filename stem length.
const maxDocumentStemRunes = 80

// SanitizeDocumentFilename reduces an untrusted filename to a safe stem made
// of letters, digits, '-' and '_'. Path components, traversal markers and the
// extension are dropped (callers append the canonical extension). Returns ""
// when nothing usable remains.
func SanitizeDocumentFilename(name string) string {
	name = strings.TrimSpace(name)
	// Treat both separators as path separators regardless of OS, then keep
	// only the final element.
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, filepath.Ext(name))

	var b strings.Builder
	n := 0
	lastDash := true // suppress leading separators
	for _, r := range name {
		if n >= maxDocumentStemRunes {
			break
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
			n++
		case r == '_' && !lastDash:
			b.WriteRune(r)
			lastDash = true
			n++
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
				n++
			}
		}
	}
	return strings.Trim(b.String(), "-_")
}

// PersistToolDocument stores a tool-produced document in the run's workspace
// uploads directory — the same location channel uploads are persisted to —
// and registers it with the run document registry so read_document(media_id)
// can resolve it. Only PDFs are accepted; the caller is responsible for any
// per-source policy (trust gating, per-call count limits).
func PersistToolDocument(ctx context.Context, data []byte, filename, mime string) (*StoredDocument, error) {
	if mime != "application/pdf" {
		return nil, fmt.Errorf("unsupported document type %q", mime)
	}
	if !IsPDF(data) {
		return nil, errors.New("content is not a PDF (missing %PDF- signature)")
	}
	if len(data) > DocumentMaxBytes {
		return nil, fmt.Errorf("document too large: %d bytes (max %d)", len(data), DocumentMaxBytes)
	}
	workspace := ToolWorkspaceFromCtx(ctx)
	if workspace == "" {
		return nil, errors.New("no workspace available to store the document")
	}

	uploadsDir := filepath.Join(workspace, ".uploads")
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	// Refuse a symlinked .uploads (Lstat does not follow links).
	fi, err := os.Lstat(uploadsDir)
	if err != nil {
		return nil, fmt.Errorf("stat uploads dir: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return nil, errors.New("uploads dir is not a regular directory")
	}

	id := uuid.New().String()
	stem := SanitizeDocumentFilename(filename)
	diskName := id + ".pdf"
	displayName := "document.pdf"
	if stem != "" {
		diskName = stem + "-" + randomHex(4) + ".pdf"
		displayName = stem + ".pdf"
	}
	dst := filepath.Join(uploadsDir, diskName)
	if filepath.Dir(filepath.Clean(dst)) != filepath.Clean(uploadsDir) {
		return nil, errors.New("refusing to store document outside uploads dir")
	}

	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create document file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(dst)
		return nil, fmt.Errorf("write document file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(dst)
		return nil, fmt.Errorf("close document file: %w", err)
	}

	ref := providers.MediaRef{ID: id, MimeType: mime, Kind: "document", Path: dst}
	if rd := RunDocumentsFromCtx(ctx); rd != nil {
		rd.Add(ref)
	}
	return &StoredDocument{
		Ref:         ref,
		Name:        displayName,
		LogicalPath: filepath.ToSlash(filepath.Join(".uploads", diskName)),
		Size:        len(data),
	}, nil
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return uuid.New().String()[:nBytes*2]
	}
	return hex.EncodeToString(b)
}
