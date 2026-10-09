package agent

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// Documents a tool stores for the agent (e.g. MCP embedded PDFs) ride on the
// tool message so later turns can still resolve them, and are never queued
// for outbound delivery to the user.
func TestProcessToolResultAttachesDocumentRefs(t *testing.T) {
	ref := providers.MediaRef{ID: "doc-1", Kind: "document", MimeType: "application/pdf", Path: "/ws/.uploads/a.pdf"}
	state := &runState{}
	toolMsg, _, _ := (&Loop{id: "test-agent"}).processToolResult(
		tools.WithToolWorkspace(context.Background(), t.TempDir()),
		state,
		&RunRequest{RunID: "run-docs"},
		func(AgentEvent) {},
		providers.ToolCall{ID: "mcp-1", Name: "mcp_filings__get_filing"},
		"mcp_filings__get_filing",
		&tools.Result{ForLLM: "[document attached: media_id=doc-1]", DocumentRefs: []providers.MediaRef{ref}},
		false,
	)

	if len(toolMsg.MediaRefs) != 1 || toolMsg.MediaRefs[0] != ref {
		t.Fatalf("tool message MediaRefs = %+v, want [%+v]", toolMsg.MediaRefs, ref)
	}
	if len(state.mediaResults) != 0 {
		t.Fatalf("document refs must not be queued for delivery, got %d", len(state.mediaResults))
	}

	// Next turn: history refs from tool messages are offered to read_document.
	history := []providers.Message{{Role: "user", Content: "get the filing"}, toolMsg}
	refs := collectRefsByKind(history, nil, "document")
	if len(refs) != 1 || refs[0].ID != "doc-1" {
		t.Fatalf("collectRefsByKind = %+v", refs)
	}
}
