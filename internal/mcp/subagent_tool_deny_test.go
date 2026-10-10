package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// subagents_config.toolDeny matches real bridge tools both by the prefixed
// name the subagent sees and by the server's original tool name.
func TestSubagentToolDenyMatchesBridgeToolNames(t *testing.T) {
	bridge := func(name string) *BridgeTool {
		tool := mcpgo.Tool{Name: name, InputSchema: mcpgo.ToolInputSchema{Type: "object"}}
		return NewBridgeTool("vn", tool, nil, "vn", 30, nil, uuid.New(), nil)
	}
	propose := bridge("vn_decision_propose")
	holdings := bridge("vn_portfolio_set_holdings")
	quote := bridge("vn_market_quote")
	if propose.Name() != "mcp_vn__vn_decision_propose" || propose.OriginalName() != "vn_decision_propose" {
		t.Fatalf("bridge naming precondition: %q / %q", propose.Name(), propose.OriginalName())
	}

	parent := tools.NewRegistry()
	parent.Register(propose)
	parent.Register(holdings)
	parent.Register(quote)
	pe := tools.NewPolicyEngine(&config.ToolsConfig{})
	allowed := map[string]bool{}
	for _, td := range pe.FilterTools(parent, "parent", "grant-script", nil, nil, false, false) {
		allowed[td.Function.Name] = true
	}

	provider := &grantScriptProvider{
		calls: []providers.ToolCall{
			{ID: "1", Name: propose.Name(), Arguments: map[string]any{"q": "x"}},
			{ID: "2", Name: holdings.Name(), Arguments: map[string]any{"q": "x"}},
		},
		results: map[string]string{},
	}
	manager := tools.NewSubagentManager(provider, nil, "grant-model", nil, nil, tools.SubagentConfig{
		MaxConcurrent: 4, MaxSpawnDepth: 1, MaxChildrenPerAgent: 4,
	})
	manager.SetParentToolsFactory(func(p *tools.Registry) *tools.Registry { return p.Clone() })

	ctx := store.WithTenantID(context.Background(), uuid.New())
	ctx = store.WithAgentID(ctx, uuid.New())
	ctx = store.WithAgentContextWindow(ctx, 200_000)
	ctx = store.WithAgentMaxTokens(ctx, 32_000)
	ctx = tools.WithToolAgentKey(ctx, "parent")
	ctx = tools.WithSubagentToolSource(ctx, parent)
	ctx = store.WithAvailableToolNames(ctx, allowed)
	ctx = tools.WithSubagentConfig(ctx, &config.SubagentsConfig{
		ToolDeny: []string{"vn_decision_*", "mcp_*__vn_portfolio_set_*"},
	})

	if _, _, _, err := manager.RunSync(ctx, "parent", 0, "scout the market", "scout", "", "test", "chat"); err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if want := []string{quote.Name()}; !slices.Equal(provider.shown, want) {
		t.Fatalf("subagent was shown %v, want %v", provider.shown, want)
	}
	for _, denied := range []*BridgeTool{propose, holdings} {
		if res := provider.results[denied.Name()]; !strings.Contains(res, "not available to this subagent") {
			t.Fatalf("%s result = %q, want rejection before execution", denied.Name(), res)
		}
	}
}
