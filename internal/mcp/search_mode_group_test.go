package mcp

import (
	"fmt"
	"slices"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// setupOverLimitManager registers more MCP tools than the inline limit on a
// single fake server and enters search mode through the real code path.
func setupOverLimitManager(t *testing.T, total int) (*Manager, *tools.Registry) {
	t.Helper()
	reg := tools.NewRegistry()
	m := NewManager(reg)

	names := make([]string, 0, total)
	for i := range total {
		bt := makeBridgeTool("svc", fmt.Sprintf("tool_%03d", i))
		reg.Register(bt)
		names = append(names, bt.Name())
	}
	m.mu.Lock()
	m.servers["svc"] = &serverState{name: "svc", toolNames: names}
	m.mu.Unlock()
	m.updateMCPGroup()

	m.maybeEnterSearchMode()
	if !m.IsSearchMode() {
		t.Fatalf("expected search mode with %d tools", total)
	}
	return m, reg
}

func mcpGroup(t *testing.T, reg *tools.Registry) map[string]bool {
	t.Helper()
	members, ok := reg.GetToolGroup("mcp")
	if !ok {
		t.Fatal(`"mcp" group not registered`)
	}
	set := make(map[string]bool, len(members))
	for _, n := range members {
		set[n] = true
	}
	return set
}

func deferredNames(m *Manager) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.deferredTools))
	for n := range m.deferredTools {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func TestSearchMode_ActivationKeepsInlineToolsInMCPGroup(t *testing.T) {
	limit := mcpToolInlineMaxCount
	m, reg := setupOverLimitManager(t, limit+5)

	m.mu.RLock()
	inline := append([]string(nil), m.inlineToolNames...)
	m.mu.RUnlock()
	if len(inline) != limit {
		t.Fatalf("inline tools = %d, want %d", len(inline), limit)
	}
	deferred := deferredNames(m)
	if len(deferred) != 5 {
		t.Fatalf("deferred tools = %d, want 5", len(deferred))
	}

	// Lazy single activation (registry deferred activator path) and the
	// explicit batch path (mcp_tool_search) must both keep the inline tools.
	reg.SetDeferredActivator(m.ActivateToolIfDeferred)
	if !reg.TryActivateDeferred(deferred[0]) {
		t.Fatalf("TryActivateDeferred(%q) failed", deferred[0])
	}
	m.ActivateTools([]string{deferred[1]})

	group := mcpGroup(t, reg)
	for _, n := range inline {
		if !group[n] {
			t.Errorf("inline tool %q dropped from group:mcp after activation", n)
		}
	}
	for _, n := range deferred[:2] {
		if !group[n] {
			t.Errorf("activated tool %q missing from group:mcp", n)
		}
	}
	for _, n := range deferred[2:] {
		if group[n] {
			t.Errorf("still-deferred tool %q should not be in group:mcp", n)
		}
	}

	// Policy: an agent allowed only group:mcp still sees the inline tools
	// plus the activated ones.
	pe := tools.NewPolicyEngine(&config.ToolsConfig{})
	allowed := map[string]bool{}
	for _, td := range pe.FilterTools(reg, "agent", "provider",
		&config.ToolPolicySpec{Allow: []string{"group:mcp"}}, nil, false, false) {
		allowed[td.Function.Name] = true
	}
	for _, n := range append(inline, deferred[:2]...) {
		if !allowed[n] {
			t.Errorf("policy group:mcp should allow %q", n)
		}
	}
	if len(allowed) != limit+2 {
		t.Errorf("policy allowed %d tools, want %d", len(allowed), limit+2)
	}
}

func TestSearchMode_ActivationKeepsMergedPerUserTools(t *testing.T) {
	m, reg := setupOverLimitManager(t, mcpToolInlineMaxCount+2)

	// The agent loop merges per-user MCP tools into the same group.
	userTool := makeBridgeTool("usersvc", "lookup").Name()
	reg.MergeToolGroup("mcp", []string{userTool})

	m.ActivateTools(deferredNames(m)[:1])
	if !mcpGroup(t, reg)[userTool] {
		t.Errorf("per-user tool %q dropped from group:mcp after activation", userTool)
	}
}

func TestSearchMode_UpdateMCPGroupKeepsActivatedTools(t *testing.T) {
	m, reg := setupOverLimitManager(t, mcpToolInlineMaxCount+2)
	activated := deferredNames(m)[0]
	m.ActivateTools([]string{activated})

	// A later server connect rebuilds the group from per-server lists.
	m.updateMCPGroup()
	group := mcpGroup(t, reg)
	if !group[activated] {
		t.Errorf("activated tool %q dropped by updateMCPGroup", activated)
	}
	m.mu.RLock()
	inline := append([]string(nil), m.inlineToolNames...)
	m.mu.RUnlock()
	for _, n := range inline {
		if !group[n] {
			t.Errorf("inline tool %q dropped by updateMCPGroup", n)
		}
	}
}
