package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

var runDocTestPDF = []byte("%PDF-1.7\n%%EOF\n")

func TestPersistToolDocumentResolvesByMediaID(t *testing.T) {
	workspace := t.TempDir()
	rd := NewRunDocuments()
	ctx := WithToolWorkspace(context.Background(), workspace)
	ctx = WithRunDocuments(ctx, rd)

	doc, err := PersistToolDocument(ctx, runDocTestPDF, "../../secret/Annual Report 2024.pdf", "application/pdf")
	if err != nil {
		t.Fatalf("PersistToolDocument: %v", err)
	}
	if doc.Name != "Annual-Report-2024.pdf" {
		t.Fatalf("name = %q", doc.Name)
	}
	if !strings.HasPrefix(doc.LogicalPath, ".uploads/Annual-Report-2024-") || !strings.HasSuffix(doc.LogicalPath, ".pdf") {
		t.Fatalf("logical path = %q", doc.LogicalPath)
	}

	tool := NewReadDocumentTool(nil, nil)
	gotPath, gotMime, err := tool.resolveDocumentFile(ctx, doc.Ref.ID, "")
	if err != nil {
		t.Fatalf("resolveDocumentFile(media_id): %v", err)
	}
	wantPath, _ := filepath.EvalSymlinks(doc.Ref.Path)
	if gotPath != wantPath || gotMime != "application/pdf" {
		t.Fatalf("resolved (%q, %q), want (%q, application/pdf)", gotPath, gotMime, wantPath)
	}

	// The logical path works too (read_document(path=...)).
	if p, _, err := tool.resolveDocumentFile(ctx, "", doc.LogicalPath); err != nil || p != wantPath {
		t.Fatalf("resolve by path = %q, %v", p, err)
	}
}

func TestAvailableDocumentRefsOrdersRunDocumentsLast(t *testing.T) {
	workspace := t.TempDir()
	userDoc := filepath.Join(workspace, ".uploads", "user.pdf")
	if err := os.MkdirAll(filepath.Dir(userDoc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userDoc, runDocTestPDF, 0o644); err != nil {
		t.Fatal(err)
	}
	userRef := providers.MediaRef{ID: uuid.NewString(), Kind: "document", MimeType: "application/pdf", Path: userDoc}

	rd := NewRunDocuments()
	ctx := WithToolWorkspace(context.Background(), workspace)
	ctx = WithMediaDocRefs(ctx, []providers.MediaRef{userRef})
	ctx = WithRunDocuments(ctx, rd)

	doc, err := PersistToolDocument(ctx, runDocTestPDF, "tool.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	refs := availableDocumentRefs(ctx)
	if len(refs) != 2 || refs[0].ID != userRef.ID || refs[1].ID != doc.Ref.ID {
		t.Fatalf("refs = %+v", refs)
	}
	// The conversation slice in ctx must not be mutated by the merge.
	if got := MediaDocRefsFromCtx(ctx); len(got) != 1 {
		t.Fatalf("ctx refs mutated: %+v", got)
	}

	// Omitted media_id picks the newest document: the tool-produced one.
	tool := NewReadDocumentTool(nil, nil)
	p, _, err := tool.resolveDocumentFile(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(doc.Ref.Path); p != want {
		t.Fatalf("default doc = %q, want %q", p, want)
	}
}

func TestPersistToolDocumentRejects(t *testing.T) {
	workspace := t.TempDir()
	ctx := WithToolWorkspace(context.Background(), workspace)

	big := make([]byte, DocumentMaxBytes+1)
	copy(big, runDocTestPDF)
	cases := []struct {
		name string
		ctx  context.Context
		data []byte
		mime string
		want string
	}{
		{"wrong mime", ctx, runDocTestPDF, "application/zip", "unsupported document type"},
		{"bad magic", ctx, []byte("PK\x03\x04zip"), "application/pdf", "not a PDF"},
		{"oversize", ctx, big, "application/pdf", "too large"},
		{"no workspace", context.Background(), runDocTestPDF, "application/pdf", "no workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PersistToolDocument(tc.ctx, tc.data, "x.pdf", tc.mime)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(workspace, ".uploads")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(workspace, ".uploads"))
		if len(entries) != 0 {
			t.Fatalf("rejected documents left files: %d", len(entries))
		}
	}
}

func TestPersistToolDocumentRefusesSymlinkedUploads(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, ".uploads")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	ctx := WithToolWorkspace(context.Background(), workspace)
	if _, err := PersistToolDocument(ctx, runDocTestPDF, "x.pdf", "application/pdf"); err == nil {
		t.Fatal("expected refusal for symlinked .uploads")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote through symlink: %d files", len(entries))
	}
}

func TestSanitizeDocumentFilename(t *testing.T) {
	cases := map[string]string{
		"":                                "",
		"..":                              "",
		"../../etc/passwd":                "passwd",
		`C:\Windows\evil.pdf`:             "evil",
		"report.final.pdf":                "report-final",
		"  Báo cáo Q3 2024.pdf ":          "Báo-cáo-Q3-2024",
		"a\x00b\nc.pdf":                   "a-b-c",
		"___.pdf":                         "",
		strings.Repeat("x", 300) + ".pdf": strings.Repeat("x", maxDocumentStemRunes),
	}
	for in, want := range cases {
		if got := SanitizeDocumentFilename(in); got != want {
			t.Errorf("SanitizeDocumentFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunDocumentsConcurrentAdd(t *testing.T) {
	rd := NewRunDocuments()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rd.Add(providers.MediaRef{ID: uuid.NewString(), Kind: "document"})
		}()
	}
	wg.Wait()
	if got := len(rd.Refs()); got != 50 {
		t.Fatalf("refs = %d, want 50", got)
	}
}
