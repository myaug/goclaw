package mcp

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// recordingGrantChecker denies every tool not in allow and records the agent
// each check was made for.
type recordingGrantChecker struct {
	mu     sync.Mutex
	allow  map[string]bool
	agents []uuid.UUID
}

func (g *recordingGrantChecker) IsAllowed(_ context.Context, agentID uuid.UUID, _ string, _ uuid.UUID, toolName string) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.agents = append(g.agents, agentID)
	if g.allow[toolName] {
		return true, ""
	}
	return false, "tool_not_in_allow_list"
}

// grantScriptProvider makes the subagent call the given tools once, then stop.
type grantScriptProvider struct {
	mu      sync.Mutex
	calls   []providers.ToolCall
	shown   []string
	results map[string]string
}

func (p *grantScriptProvider) Name() string         { return "grant-script" }
func (p *grantScriptProvider) DefaultModel() string { return "grant-model" }
func (p *grantScriptProvider) ChatStream(ctx context.Context, req providers.ChatRequest, _ func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return p.Chat(ctx, req)
}

func (p *grantScriptProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	turn := 0
	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			turn++
		case "tool":
			p.results[m.ToolName] = m.Content
		}
	}
	if turn == 0 {
		for _, td := range req.Tools {
			if td.Function != nil {
				p.shown = append(p.shown, td.Function.Name)
			}
		}
		return &providers.ChatResponse{ToolCalls: p.calls, FinishReason: "tool_calls"}, nil
	}
	return &providers.ChatResponse{Content: "done", FinishReason: "stop"}, nil
}

func grantTestBridge(name string, gc GrantChecker) *BridgeTool {
	tool := mcpgo.Tool{Name: name, InputSchema: mcpgo.ToolInputSchema{Type: "object"}}
	return NewBridgeTool("docs", tool, nil, "", 30, nil, uuid.New(), gc)
}

// A subagent's MCP tools are the parent agent's grant-checked bridge tools: a
// tool the parent's grant does not cover is absent, and a tool whose grant is
// revoked is rejected by the same execution-time grant check, keyed by the
// parent (root) agent.
func TestSubagentMCPToolsUseParentGrantCheck(t *testing.T) {
	gc := &recordingGrantChecker{allow: map[string]bool{}}

	// Gateway-wide registry: every tool of the server, no grant checker.
	gateway := tools.NewRegistry()
	ungranted := grantTestBridge("admin", nil)
	gateway.Register(ungranted)
	gateway.Register(grantTestBridge("search", nil))

	// Parent's per-agent registry: only the granted tool, with the grant checker.
	// Its grant is then revoked (gc.allow is empty).
	search := grantTestBridge("search", gc)
	parent := gateway.Clone()
	parent.Unregister(ungranted.Name())
	parent.Register(search)

	pe := tools.NewPolicyEngine(&config.ToolsConfig{})
	allowed := map[string]bool{}
	for _, td := range pe.FilterTools(parent, "parent", "grant-script", nil, nil, false, false) {
		allowed[td.Function.Name] = true
	}

	provider := &grantScriptProvider{
		calls: []providers.ToolCall{
			{ID: "1", Name: ungranted.Name(), Arguments: map[string]any{"q": "x"}},
			{ID: "2", Name: search.Name(), Arguments: map[string]any{"q": "x"}},
		},
		results: map[string]string{},
	}
	manager := tools.NewSubagentManager(provider, nil, "grant-model", nil, gateway.Clone, tools.SubagentConfig{
		MaxConcurrent: 4, MaxSpawnDepth: 1, MaxChildrenPerAgent: 4,
	})
	manager.SetParentToolsFactory(func(p *tools.Registry) *tools.Registry { return p.Clone() })

	parentAgent := uuid.New()
	ctx := store.WithTenantID(context.Background(), uuid.New())
	ctx = store.WithAgentID(ctx, parentAgent)
	ctx = store.WithAgentContextWindow(ctx, 200_000)
	ctx = store.WithAgentMaxTokens(ctx, 32_000)
	ctx = tools.WithToolAgentKey(ctx, "parent")
	ctx = tools.WithSubagentToolSource(ctx, parent)
	ctx = store.WithAvailableToolNames(ctx, allowed)

	if _, _, _, err := manager.RunSync(ctx, "parent", 0, "look up docs", "docs", "", "test", "chat"); err != nil {
		t.Fatalf("RunSync: %v", err)
	}

	if slices.Contains(provider.shown, ungranted.Name()) {
		t.Fatalf("subagent was shown MCP tool outside the parent's grant: %v", provider.shown)
	}
	if res := provider.results[ungranted.Name()]; !strings.Contains(res, "not available to this subagent") {
		t.Fatalf("ungranted tool result = %q, want rejection", res)
	}
	if res := provider.results[search.Name()]; !strings.Contains(res, "grant revoked") {
		t.Fatalf("revoked tool result = %q, want grant re-check rejection", res)
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if len(gc.agents) != 1 || gc.agents[0] != parentAgent {
		t.Fatalf("grant checks = %v, want one check for parent agent %s", gc.agents, parentAgent)
	}
}
