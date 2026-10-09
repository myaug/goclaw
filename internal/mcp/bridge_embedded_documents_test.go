package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

var testPDF = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")

func pdfResource(uri, mime string, data []byte) mcpgo.EmbeddedResource {
	return mcpgo.NewEmbeddedResource(mcpgo.BlobResourceContents{
		URI:      uri,
		MIMEType: mime,
		Blob:     base64.StdEncoding.EncodeToString(data),
	})
}

func newDocTestTool(allow bool) *BridgeTool {
	tool := mcpgo.Tool{Name: "get_filing", InputSchema: mcpgo.ToolInputSchema{Type: "object"}}
	return NewBridgeTool("filings", tool, nil, "", 30, nil, uuid.Nil, nil).WithEmbeddedDocuments(allow)
}

func docTestCtx(t *testing.T) (context.Context, string, *tools.RunDocuments) {
	t.Helper()
	workspace := t.TempDir()
	rd := tools.NewRunDocuments()
	ctx := tools.WithToolWorkspace(context.Background(), workspace)
	ctx = tools.WithRunDocuments(ctx, rd)
	return ctx, workspace, rd
}

func uploadsCount(t *testing.T, workspace string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(workspace, ".uploads"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestConvertResultContent_FlagOffKeepsPlaceholder(t *testing.T) {
	ctx, workspace, rd := docTestCtx(t)
	bt := newDocTestTool(false)
	result := &mcpgo.CallToolResult{Content: []mcpgo.Content{
		mcpgo.TextContent{Type: "text", Text: "filing found"},
		pdfResource("example://filings/report.pdf", "application/pdf", testPDF),
	}}

	text, refs := bt.convertResultContent(ctx, result)
	if want := extractTextContent(result); text != want {
		t.Fatalf("flag off text = %q, want legacy %q", text, want)
	}
	if !strings.Contains(text, "[non-text content:") {
		t.Fatalf("flag off should keep non-text placeholder, got %q", text)
	}
	if len(refs) != 0 || len(rd.Refs()) != 0 || uploadsCount(t, workspace) != 0 {
		t.Fatalf("flag off must not store documents (refs=%d registry=%d files=%d)",
			len(refs), len(rd.Refs()), uploadsCount(t, workspace))
	}
}

func TestConvertResultContent_FlagOnStoresPDF(t *testing.T) {
	ctx, workspace, rd := docTestCtx(t)
	bt := newDocTestTool(true)
	result := &mcpgo.CallToolResult{Content: []mcpgo.Content{
		mcpgo.TextContent{Type: "text", Text: "filing found"},
		pdfResource("example://filings/../../etc/Báo cáo tài chính 2024.pdf", "application/pdf", testPDF),
	}}

	text, refs := bt.convertResultContent(ctx, result)
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1 (text %q)", len(refs), text)
	}
	ref := refs[0]
	if ref.Kind != "document" || ref.MimeType != "application/pdf" || ref.ID == "" {
		t.Fatalf("unexpected ref %+v", ref)
	}
	if !strings.HasPrefix(text, "filing found\n[document attached: media_id="+ref.ID) {
		t.Fatalf("missing attachment line, got %q", text)
	}
	if !strings.Contains(text, "name=Báo-cáo-tài-chính-2024.pdf") {
		t.Fatalf("expected sanitized name in %q", text)
	}
	if got := rd.Refs(); len(got) != 1 || got[0].ID != ref.ID {
		t.Fatalf("run registry = %+v, want the stored ref", got)
	}
	wantDir := filepath.Join(workspace, ".uploads")
	if filepath.Dir(ref.Path) != wantDir {
		t.Fatalf("stored at %q, want inside %q", ref.Path, wantDir)
	}
	data, err := os.ReadFile(ref.Path)
	if err != nil || string(data) != string(testPDF) {
		t.Fatalf("stored bytes mismatch (err=%v)", err)
	}
}

func TestConvertResultContent_RejectsWrongMimeAndMagic(t *testing.T) {
	ctx, workspace, _ := docTestCtx(t)
	bt := newDocTestTool(true)
	result := &mcpgo.CallToolResult{Content: []mcpgo.Content{
		pdfResource("x://a.zip", "application/zip", testPDF),
		pdfResource("x://fake.pdf", "application/pdf", []byte("MZ\x90\x00 not a pdf")),
		pdfResource("x://bad.pdf", "application/pdf", nil),
	}}
	// Corrupt base64 on a separate resource.
	result.Content = append(result.Content, mcpgo.NewEmbeddedResource(mcpgo.BlobResourceContents{
		URI: "x://garbage.pdf", MIMEType: "application/pdf", Blob: "!!!not-base64!!!",
	}))

	text, refs := bt.convertResultContent(ctx, result)
	if len(refs) != 0 || uploadsCount(t, workspace) != 0 {
		t.Fatalf("nothing should be stored, got refs=%d files=%d", len(refs), uploadsCount(t, workspace))
	}
	for _, want := range []string{"unsupported mimeType", "missing %PDF- signature", "not valid base64"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected rejection %q in %q", want, text)
		}
	}
}

func TestConvertResultContent_RejectsOversize(t *testing.T) {
	ctx, workspace, _ := docTestCtx(t)
	bt := newDocTestTool(true)
	big := make([]byte, tools.DocumentMaxBytes+1)
	copy(big, testPDF)
	result := &mcpgo.CallToolResult{Content: []mcpgo.Content{
		pdfResource("x://big.pdf", "application/pdf", big),
	}}

	text, refs := bt.convertResultContent(ctx, result)
	if len(refs) != 0 || uploadsCount(t, workspace) != 0 {
		t.Fatalf("oversize document must not be stored")
	}
	if !strings.Contains(text, "too large") {
		t.Fatalf("expected size rejection, got %q", text)
	}
}

func TestConvertResultContent_PerCallLimit(t *testing.T) {
	ctx, workspace, _ := docTestCtx(t)
	bt := newDocTestTool(true)
	result := &mcpgo.CallToolResult{}
	for i := 0; i < maxEmbeddedDocumentsPerCall+2; i++ {
		result.Content = append(result.Content, pdfResource("x://doc.pdf", "application/pdf", testPDF))
	}

	text, refs := bt.convertResultContent(ctx, result)
	if len(refs) != maxEmbeddedDocumentsPerCall {
		t.Fatalf("stored %d, want %d", len(refs), maxEmbeddedDocumentsPerCall)
	}
	if got := uploadsCount(t, workspace); got != maxEmbeddedDocumentsPerCall {
		t.Fatalf("files = %d, want %d", got, maxEmbeddedDocumentsPerCall)
	}
	if strings.Count(text, "documents per tool call reached") != 2 {
		t.Fatalf("expected 2 limit rejections, got %q", text)
	}
}

// TestConvertResultContent_ParsedFromWireJSON checks the shape a non-Go server
// (e.g. the Python SDK) puts on the wire is recognised after mcp-go parsing.
func TestConvertResultContent_ParsedFromWireJSON(t *testing.T) {
	ctx, _, _ := docTestCtx(t)
	bt := newDocTestTool(true)
	wire := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "ok"},
			map[string]any{"type": "resource", "resource": map[string]any{
				"uri":      "example://filings/ABC/2024-annual.pdf",
				"mimeType": "application/pdf",
				"blob":     base64.StdEncoding.EncodeToString(testPDF),
			}},
		},
		"isError": false,
	}
	raw, _ := json.Marshal(wire)
	rm := json.RawMessage(raw)
	result, err := mcpgo.ParseCallToolResult(&rm)
	if err != nil {
		t.Fatal(err)
	}

	text, refs := bt.convertResultContent(ctx, result)
	if len(refs) != 1 || !strings.Contains(text, "name=2024-annual.pdf") {
		t.Fatalf("wire JSON resource not stored: refs=%d text=%q", len(refs), text)
	}
}

func TestParseAllowEmbeddedDocuments(t *testing.T) {
	cases := map[string]bool{
		``:                                     false,
		`{}`:                                   false,
		`{"allow_embedded_documents": false}`:  false,
		`{"allow_embedded_documents": "true"}`: false,
		`not json`:                             false,
		`{"allow_embedded_documents": true}`:   true,
		`{"tool_hints":{"global":"x"},"allow_embedded_documents":true}`: true,
	}
	for in, want := range cases {
		if got := ParseAllowEmbeddedDocuments(json.RawMessage(in)); got != want {
			t.Errorf("ParseAllowEmbeddedDocuments(%q) = %v, want %v", in, got, want)
		}
	}
	bs := ParseBridgeSettings(json.RawMessage(`{"tool_hints":{"global":"g"},"allow_embedded_documents":true}`))
	if !bs.AllowEmbeddedDocuments || bs.Hints.Global != "g" {
		t.Fatalf("ParseBridgeSettings = %+v", bs)
	}
}

// TestBridgeToolExecute_EmbeddedDocumentReadableByReadDocument runs a real
// (in-process) MCP server through BridgeTool.Execute and then resolves the
// returned media_id through read_document.
func TestBridgeToolExecute_EmbeddedDocumentReadableByReadDocument(t *testing.T) {
	srv := mcpserver.NewMCPServer("filings", "1.0.0", mcpserver.WithToolCapabilities(false))
	mcpTool := mcpgo.NewTool("get_filing")
	srv.AddTool(mcpTool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return &mcpgo.CallToolResult{Content: []mcpgo.Content{
			mcpgo.TextContent{Type: "text", Text: "annual report"},
			pdfResource("example://filings/annual.pdf", "application/pdf", testPDF),
		}}, nil
	})

	client, err := mcpclient.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	initReq := mcpgo.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpgo.Implementation{Name: "test", Version: "1"}
	if _, err := client.Initialize(ctx, initReq); err != nil {
		t.Fatal(err)
	}

	var ptr atomic.Pointer[mcpclient.Client]
	ptr.Store(client)
	var connected atomic.Bool
	connected.Store(true)

	for _, allow := range []bool{false, true} {
		runCtx, workspace, rd := docTestCtx(t)
		bt := NewBridgeTool("filings", mcpTool, &ptr, "", 30, &connected, uuid.Nil, nil).WithEmbeddedDocuments(allow)
		res := bt.Execute(runCtx, map[string]any{})
		if res.IsError {
			t.Fatalf("allow=%v: Execute error: %s", allow, res.ForLLM)
		}
		if !allow {
			if len(res.DocumentRefs) != 0 || uploadsCount(t, workspace) != 0 ||
				!strings.Contains(res.ForLLM, "[non-text content:") {
				t.Fatalf("flag off: unexpected result %q refs=%d", res.ForLLM, len(res.DocumentRefs))
			}
			continue
		}
		if len(res.DocumentRefs) != 1 {
			t.Fatalf("flag on: refs = %d, result %q", len(res.DocumentRefs), res.ForLLM)
		}
		id := res.DocumentRefs[0].ID
		if !strings.Contains(res.ForLLM, "media_id="+id) ||
			!strings.Contains(res.ForLLM, "<<<EXTERNAL_UNTRUSTED_CONTENT>>>") {
			t.Fatalf("flag on: attachment line missing or unwrapped: %q", res.ForLLM)
		}
		if len(rd.Refs()) != 1 {
			t.Fatalf("run registry not populated")
		}

		// read_document resolves the media_id with no conversation refs —
		// the document only exists in the run registry. A fake local parser
		// stands in for the extractor/LLM so the read is end-to-end.
		rt := tools.NewReadDocumentTool(nil, nil)
		rt.SetLocalParser(&fakePDFParser{})
		read := rt.Execute(runCtx, map[string]any{"prompt": "summarize", "media_id": id})
		if read.IsError || read.ForLLM != "pdf text: "+string(testPDF) {
			t.Fatalf("read_document(media_id) = %q (error=%v)", read.ForLLM, read.IsError)
		}
		missing := rt.Execute(runCtx, map[string]any{"prompt": "x", "media_id": "nope"})
		if !missing.IsError || !strings.Contains(missing.ForLLM, "not found") {
			t.Fatalf("expected not-found for unknown id, got %q", missing.ForLLM)
		}
	}
}

// fakePDFParser is a tools.DocumentParser that echoes the file content.
type fakePDFParser struct{}

func (fakePDFParser) Supports(mime string) bool { return mime == "application/pdf" }

func (fakePDFParser) Extract(_ context.Context, path, _ string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "pdf text: " + string(data), nil
}
