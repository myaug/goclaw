package mcp

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	vaultLookup   = "mcp_vault__lookup"
	vaultTransfer = "mcp_vault__transfer"
)

// startVaultServer serves a generic MCP server with two tools, lookup and
// transfer, over streamable-http.
func startVaultServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcpserver.NewMCPServer("vault", "1.0.0", mcpserver.WithToolCapabilities(false))
	for _, name := range []string{"lookup", "transfer"} {
		srv.AddTool(mcpgo.NewTool(name, mcpgo.WithDescription(name)), func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText(req.Params.Name + " ok"), nil
		})
	}
	ts := httptest.NewServer(mcpserver.NewStreamableHTTPServer(srv))
	t.Cleanup(ts.Close)
	return ts
}

// leakySharedRegistry reproduces the old gateway wiring: a gateway-wide manager
// connecting the server into the shared registry with no grants.
func leakySharedRegistry(t *testing.T, url string) *tools.Registry {
	t.Helper()
	shared := tools.NewRegistry()
	enabled := true
	global := NewManager(shared, WithConfigs(map[string]*config.MCPServerConfig{
		"vault": {Transport: "streamable-http", URL: url, Enabled: &enabled},
	}))
	if err := global.Start(context.Background()); err != nil {
		t.Fatalf("gateway Start: %v", err)
	}
	t.Cleanup(global.Stop)
	if _, ok := shared.Get(vaultTransfer); !ok {
		t.Fatalf("precondition: shared registry = %v, want gateway MCP tools", shared.List())
	}
	return shared
}

type vaultAgent struct {
	id       uuid.UUID
	reg      *tools.Registry
	store    *mockMCPStore
	checker  *storeGrantChecker
	serverID uuid.UUID
}

// loadVaultAgent builds an agent registry the way the resolver does: clone the
// shared registry for the agent, then load its granted MCP servers through a
// per-agent manager with the store-backed grant checker.
func loadVaultAgent(t *testing.T, shared *tools.Registry, url string, grants []store.MCPAccessInfo) *vaultAgent {
	t.Helper()
	a := &vaultAgent{id: uuid.New(), store: &mockMCPStore{accessible: grants}}
	a.checker = NewStoreGrantChecker(a.store, nil)
	a.reg = CloneForAgent(shared)
	mgr := NewManager(a.reg, WithStore(a.store), WithGrantChecker(a.checker))
	if err := mgr.LoadForAgent(context.Background(), a.id, ""); err != nil {
		t.Fatalf("LoadForAgent: %v", err)
	}
	t.Cleanup(mgr.Stop)
	return a
}

func vaultGrant(serverID uuid.UUID, url string, allow, deny []string) store.MCPAccessInfo {
	return store.MCPAccessInfo{
		Server: store.MCPServerData{
			BaseModel: store.BaseModel{ID: serverID},
			Name:      "vault",
			Transport: "streamable-http",
			URL:       url,
			Enabled:   true,
		},
		ToolAllow: allow,
		ToolDeny:  deny,
	}
}

func (a *vaultAgent) ctx() context.Context {
	ctx := store.WithTenantID(context.Background(), uuid.New())
	ctx = store.WithAgentID(ctx, a.id)
	ctx = store.WithAgentContextWindow(ctx, 200_000)
	ctx = store.WithAgentMaxTokens(ctx, 32_000)
	return tools.WithToolAgentKey(ctx, "agent-"+a.id.String()[:8])
}

func (a *vaultAgent) shownTools() map[string]bool {
	pe := tools.NewPolicyEngine(&config.ToolsConfig{})
	shown := map[string]bool{}
	for _, td := range pe.FilterTools(a.reg, "agent", "test", nil, nil, false, false) {
		shown[td.Function.Name] = true
	}
	return shown
}

func mcpToolNames(reg *tools.Registry) []string {
	var names []string
	for _, n := range reg.List() {
		if strings.HasPrefix(n, "mcp_") {
			names = append(names, n)
		}
	}
	return names
}

func TestAgentRegistryHoldsOnlyGrantedMCPTools(t *testing.T) {
	ts := startVaultServer(t)
	shared := leakySharedRegistry(t, ts.URL)
	serverID := uuid.New()

	// Agent A: granted lookup only.
	a := loadVaultAgent(t, shared, ts.URL, []store.MCPAccessInfo{vaultGrant(serverID, ts.URL, []string{"lookup"}, nil)})
	if got := mcpToolNames(a.reg); !slices.Equal(got, []string{vaultLookup}) {
		t.Fatalf("agent A MCP tools = %v, want only %s", got, vaultLookup)
	}
	if shown := a.shownTools(); shown[vaultTransfer] || !shown[vaultLookup] {
		t.Fatalf("agent A tool defs = %v", shown)
	}
	if res := a.reg.Execute(a.ctx(), vaultTransfer, map[string]any{}); !res.IsError {
		t.Fatalf("agent A executed ungranted %s: %q", vaultTransfer, res.ForLLM)
	}
	lookup, _ := a.reg.Get(vaultLookup)
	if bt, ok := lookup.(*BridgeTool); !ok || bt.grantChecker == nil || bt.serverID != serverID {
		t.Fatalf("agent A %s is not its own grant-checked bridge tool: %#v", vaultLookup, lookup)
	}
	if res := a.reg.Execute(a.ctx(), vaultLookup, map[string]any{}); res.IsError || !strings.Contains(res.ForLLM, "lookup ok") {
		t.Fatalf("granted %s failed: %q", vaultLookup, res.ForLLM)
	}

	// Agent B: no grant at all.
	b := loadVaultAgent(t, shared, ts.URL, nil)
	if got := mcpToolNames(b.reg); len(got) != 0 {
		t.Fatalf("agent B without grants has MCP tools %v", got)
	}
	for _, name := range []string{vaultLookup, vaultTransfer} {
		if res := b.reg.Execute(b.ctx(), name, map[string]any{}); !res.IsError {
			t.Fatalf("agent B executed %s: %q", name, res.ForLLM)
		}
	}

	// The shared registry itself is left untouched.
	if _, ok := shared.Get(vaultTransfer); !ok {
		t.Fatal("CloneForAgent mutated the shared registry")
	}
}

func TestAgentMCPGrantRevocationTakesEffect(t *testing.T) {
	ts := startVaultServer(t)
	shared := tools.NewRegistry()

	cases := []struct {
		name   string
		revoke func(a *vaultAgent)
		reason string
	}{
		{name: "grant removed", reason: "server_not_accessible", revoke: func(a *vaultAgent) {
			a.store.accessible = nil
		}},
		{name: "tool moved to tool_deny", reason: "tool_denied", revoke: func(a *vaultAgent) {
			a.store.accessible = []store.MCPAccessInfo{vaultGrant(a.serverID, ts.URL, nil, []string{"lookup"})}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serverID := uuid.New()
			a := loadVaultAgent(t, shared, ts.URL, []store.MCPAccessInfo{vaultGrant(serverID, ts.URL, []string{"lookup"}, nil)})
			a.serverID = serverID
			if res := a.reg.Execute(a.ctx(), vaultLookup, map[string]any{}); res.IsError {
				t.Fatalf("before revoke: %q", res.ForLLM)
			}
			tc.revoke(a)
			a.checker.Invalidate() // what the grant-change cache broadcast does
			res := a.reg.Execute(a.ctx(), vaultLookup, map[string]any{})
			if !res.IsError || !strings.Contains(res.ForLLM, "grant revoked") || !strings.Contains(res.ForLLM, tc.reason) {
				t.Fatalf("after revoke: isError=%v %q, want grant revoked (%s)", res.IsError, res.ForLLM, tc.reason)
			}
		})
	}
}

// A subagent spawned by agent A gets neither the ungranted tool of the leaky
// shared registry nor anything A could not call.
func TestSubagentOfAgentLacksUngrantedMCPTool(t *testing.T) {
	ts := startVaultServer(t)
	shared := leakySharedRegistry(t, ts.URL)
	a := loadVaultAgent(t, shared, ts.URL, []store.MCPAccessInfo{vaultGrant(uuid.New(), ts.URL, []string{"lookup"}, nil)})

	provider := &grantScriptProvider{
		calls: []providers.ToolCall{
			{ID: "1", Name: vaultTransfer, Arguments: map[string]any{"q": "x"}},
			{ID: "2", Name: vaultLookup, Arguments: map[string]any{"q": "x"}},
		},
		results: map[string]string{},
	}
	// The legacy factory exposes the leaky shared registry; it must not be used.
	manager := tools.NewSubagentManager(provider, nil, "grant-model", nil, shared.Clone, tools.SubagentConfig{
		MaxConcurrent: 4, MaxSpawnDepth: 1, MaxChildrenPerAgent: 4,
	})
	manager.SetParentToolsFactory(func(p *tools.Registry) *tools.Registry { return p.Clone() })

	ctx := tools.WithSubagentToolSource(a.ctx(), a.reg)
	ctx = store.WithAvailableToolNames(ctx, a.shownTools())
	if _, _, _, err := manager.RunSync(ctx, "agent-a", 0, "use the vault", "vault", "", "test", "chat"); err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if slices.Contains(provider.shown, vaultTransfer) {
		t.Fatalf("subagent was shown %s: %v", vaultTransfer, provider.shown)
	}
	if res := provider.results[vaultTransfer]; !strings.Contains(res, "not available to this subagent") {
		t.Fatalf("subagent %s result = %q, want rejection", vaultTransfer, res)
	}
	if res := provider.results[vaultLookup]; !strings.Contains(res, "lookup ok") {
		t.Fatalf("subagent %s result = %q, want success", vaultLookup, res)
	}
}
