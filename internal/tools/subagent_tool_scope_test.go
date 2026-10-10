package tools

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// scopeScriptProvider replays a scripted conversation per subagent task. The
// task text (first user message) selects the script; the number of assistant
// turns already in the request selects the step. It records the tool names it
// was shown and the tool results it received, per task.
type scopeScriptProvider struct {
	mu      sync.Mutex
	scripts map[string][][]providers.ToolCall
	shown   map[string][]string
	results map[string]map[string]string // task -> tool name -> last tool result
}

func newScopeScriptProvider(scripts map[string][][]providers.ToolCall) *scopeScriptProvider {
	return &scopeScriptProvider{
		scripts: scripts,
		shown:   make(map[string][]string),
		results: make(map[string]map[string]string),
	}
}

func (p *scopeScriptProvider) Name() string         { return "scope-script" }
func (p *scopeScriptProvider) DefaultModel() string { return "scope-model" }
func (p *scopeScriptProvider) ChatStream(ctx context.Context, req providers.ChatRequest, _ func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return p.Chat(ctx, req)
}

func (p *scopeScriptProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	task := ""
	turn := 0
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			if task == "" {
				task = m.Content
			}
		case "assistant":
			turn++
		case "tool":
			if p.results[task] == nil {
				p.results[task] = make(map[string]string)
			}
			p.results[task][m.ToolName] = m.Content
		}
	}
	if turn == 0 {
		names := make([]string, 0, len(req.Tools))
		for _, td := range req.Tools {
			if td.Function != nil {
				names = append(names, td.Function.Name)
			}
		}
		slices.Sort(names)
		p.shown[task] = names
	}
	if steps := p.scripts[task]; turn < len(steps) {
		return &providers.ChatResponse{ToolCalls: steps[turn], FinishReason: "tool_calls"}, nil
	}
	return &providers.ChatResponse{Content: "done: " + task, FinishReason: "stop"}, nil
}

func (p *scopeScriptProvider) shownTo(task string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.shown[task]...)
}

func (p *scopeScriptProvider) resultFor(task, tool string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.results[task][tool]
}

// scopeProbeTool counts executions per instance so a test can tell which
// registry's instance a subagent actually ran.
type scopeProbeTool struct {
	name  string
	mu    sync.Mutex
	calls int
}

func newScopeProbe(name string) *scopeProbeTool { return &scopeProbeTool{name: name} }

func (t *scopeProbeTool) Name() string               { return t.name }
func (t *scopeProbeTool) Description() string        { return "probe " + t.name }
func (t *scopeProbeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *scopeProbeTool) Execute(context.Context, map[string]any) *Result {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return NewResult(t.name + " ok")
}

func (t *scopeProbeTool) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func call(id, name string, args map[string]any) providers.ToolCall {
	if args == nil {
		args = map[string]any{"x": "y"}
	}
	return providers.ToolCall{ID: id, Name: name, Arguments: args}
}

// parentAllowedTools runs the real policy engine over the parent's registry, as
// the agent loop does, and returns the resulting effective tool names.
func parentAllowedTools(reg *Registry, policy *config.ToolPolicySpec) map[string]bool {
	pe := NewPolicyEngine(&config.ToolsConfig{})
	allowed := make(map[string]bool)
	for _, td := range pe.FilterTools(reg, "parent", "scope-script", policy, nil, false, false) {
		if td.Function != nil {
			allowed[td.Function.Name] = true
		}
	}
	return allowed
}

// parentRunContext mimics what the parent agent's run publishes to a tool call.
func parentRunContext(reg *Registry, allowed map[string]bool) context.Context {
	ctx := subagentTestContext("parent")
	if reg != nil {
		ctx = WithSubagentToolSource(ctx, reg)
	}
	if allowed != nil {
		ctx = store.WithAvailableToolNames(ctx, allowed)
	}
	return ctx
}

func newScopeManager(provider providers.Provider, maxDepth int) *SubagentManager {
	return NewSubagentManager(provider, nil, "scope-model", nil, nil, SubagentConfig{
		MaxConcurrent:       8,
		MaxSpawnDepth:       maxDepth,
		MaxChildrenPerAgent: 8,
	})
}

func TestSubagentInheritsParentAllowList(t *testing.T) {
	readFile, webFetch, exec := newScopeProbe("read_file"), newScopeProbe("web_fetch"), newScopeProbe("exec")
	parent := NewRegistry()
	parent.Register(readFile)
	parent.Register(webFetch)
	parent.Register(exec)

	allowed := parentAllowedTools(parent, &config.ToolPolicySpec{Allow: []string{"group:fs"}})
	if allowed["web_fetch"] || !allowed["read_file"] {
		t.Fatalf("policy precondition: allowed = %v", allowed)
	}

	const task = "fetch something"
	provider := newScopeScriptProvider(map[string][][]providers.ToolCall{
		task: {{call("1", "web_fetch", map[string]any{"url": "https://example.invalid"}), call("2", "read_file", nil)}},
	})
	manager := newScopeManager(provider, 1)

	if _, _, _, err := manager.RunSync(parentRunContext(parent, allowed), "parent", 0, task, "t", "", "test", "chat"); err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if got := provider.shownTo(task); !slices.Equal(got, []string{"read_file"}) {
		t.Fatalf("subagent was shown %v, want only [read_file]", got)
	}
	if webFetch.count() != 0 {
		t.Fatalf("web_fetch executed %d times in subagent; parent allow-list excludes it", webFetch.count())
	}
	if res := provider.resultFor(task, "web_fetch"); !strings.Contains(res, "not available to this subagent") {
		t.Fatalf("web_fetch result = %q, want rejection", res)
	}
	if readFile.count() != 1 {
		t.Fatalf("allowed read_file executed %d times, want 1", readFile.count())
	}
}

func TestSubagentCannotExecWhenParentDeniesIt(t *testing.T) {
	readFile, exec := newScopeProbe("read_file"), newScopeProbe("exec")
	parent := NewRegistry()
	parent.Register(readFile)
	parent.Register(exec)
	parent.RegisterAlias("bash", "exec")

	// Deny by group: expansion must remove exec (and so its alias) for the parent.
	allowed := parentAllowedTools(parent, &config.ToolPolicySpec{Deny: []string{"group:runtime"}})
	if allowed["exec"] || allowed["bash"] || !allowed["read_file"] {
		t.Fatalf("policy precondition: allowed = %v", allowed)
	}

	const task = "run a command"
	provider := newScopeScriptProvider(map[string][][]providers.ToolCall{
		task: {{call("1", "exec", map[string]any{"command": "id"}), call("2", "bash", map[string]any{"command": "id"})}},
	})
	manager := newScopeManager(provider, 1)

	if _, _, _, err := manager.RunSync(parentRunContext(parent, allowed), "parent", 0, task, "t", "", "test", "chat"); err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if got := provider.shownTo(task); slices.Contains(got, "exec") || slices.Contains(got, "bash") {
		t.Fatalf("subagent was shown %v, must not include exec or its alias", got)
	}
	if exec.count() != 0 {
		t.Fatalf("exec executed %d times in subagent; parent denies it", exec.count())
	}
	for _, name := range []string{"exec", "bash"} {
		if res := provider.resultFor(task, name); !strings.Contains(res, "not available to this subagent") {
			t.Fatalf("%s result = %q, want rejection", name, res)
		}
	}
}

// The gateway-wide registry holds every MCP server's tools without grant
// checks; the parent's per-agent registry holds only the granted ones (with the
// execution-time grant re-check). The subagent must use the latter.
func TestSubagentMCPToolsFollowParentGrants(t *testing.T) {
	globalSearch, globalDelete := newScopeProbe("mcp_docs__search"), newScopeProbe("mcp_docs__delete")
	gateway := NewRegistry()
	gateway.Register(globalSearch)
	gateway.Register(globalDelete)

	parentSearch := newScopeProbe("mcp_docs__search") // grant-filtered per-agent instance
	parent := NewRegistry()
	parent.Register(newScopeProbe("read_file"))
	parent.Register(parentSearch)
	parent.RegisterToolGroup("mcp", []string{"mcp_docs__search"})

	allowed := parentAllowedTools(parent, nil)
	if allowed["mcp_docs__delete"] || !allowed["mcp_docs__search"] {
		t.Fatalf("policy precondition: allowed = %v", allowed)
	}

	const task = "use docs"
	provider := newScopeScriptProvider(map[string][][]providers.ToolCall{
		task: {{call("1", "mcp_docs__delete", nil), call("2", "mcp_docs__search", nil)}},
	})
	// Legacy gateway-wide factory still configured: it must not be used once the
	// parent's registry is known.
	manager := NewSubagentManager(provider, nil, "scope-model", nil, gateway.Clone, SubagentConfig{
		MaxConcurrent: 8, MaxSpawnDepth: 1, MaxChildrenPerAgent: 8,
	})

	if _, _, _, err := manager.RunSync(parentRunContext(parent, allowed), "parent", 0, task, "t", "", "test", "chat"); err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if got := provider.shownTo(task); slices.Contains(got, "mcp_docs__delete") {
		t.Fatalf("subagent was shown ungranted MCP tool: %v", got)
	}
	if globalDelete.count() != 0 {
		t.Fatal("ungranted MCP tool executed in subagent")
	}
	if res := provider.resultFor(task, "mcp_docs__delete"); !strings.Contains(res, "not available to this subagent") {
		t.Fatalf("mcp_docs__delete result = %q, want rejection", res)
	}
	if parentSearch.count() != 1 || globalSearch.count() != 0 {
		t.Fatalf("granted MCP tool ran parent instance %d times, gateway instance %d times; want 1/0",
			parentSearch.count(), globalSearch.count())
	}
}

// An allowed tool still works, and the factory's subagent-specific variant of
// it (e.g. a workspace-scoped file tool) is the instance that runs.
func TestSubagentAllowedToolRunsFactoryVariant(t *testing.T) {
	parentWrite := newScopeProbe("write_file")
	parent := NewRegistry()
	parent.Register(parentWrite)
	parent.Register(newScopeProbe("web_fetch"))

	scopedWrite := newScopeProbe("write_file")
	smuggled := newScopeProbe("browser")
	const task = "write a file"
	provider := newScopeScriptProvider(map[string][][]providers.ToolCall{
		task: {{call("1", "write_file", nil), call("2", "browser", nil)}},
	})
	manager := newScopeManager(provider, 1)
	manager.SetParentToolsFactory(func(p *Registry) *Registry {
		reg := p.Clone()
		reg.Register(scopedWrite)
		reg.Register(smuggled) // a factory adding tools must not widen the boundary
		return reg
	})

	allowed := parentAllowedTools(parent, &config.ToolPolicySpec{Allow: []string{"write_file"}})
	if _, _, _, err := manager.RunSync(parentRunContext(parent, allowed), "parent", 0, task, "t", "", "test", "chat"); err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if got := provider.shownTo(task); !slices.Equal(got, []string{"write_file"}) {
		t.Fatalf("subagent was shown %v, want [write_file]", got)
	}
	if scopedWrite.count() != 1 || parentWrite.count() != 0 {
		t.Fatalf("write_file ran scoped %d / parent %d times, want 1/0", scopedWrite.count(), parentWrite.count())
	}
	if smuggled.count() != 0 {
		t.Fatal("tool added by the factory but absent from parent policy executed")
	}
}

func TestNestedSubagentKeepsParentRestriction(t *testing.T) {
	readFile, webFetch := newScopeProbe("read_file"), newScopeProbe("web_fetch")
	parent := NewRegistry()
	parent.Register(readFile)
	parent.Register(webFetch)
	parent.Register(newScopeProbe("spawn")) // parent's own spawn; replaced per subagent

	allowed := parentAllowedTools(parent, &config.ToolPolicySpec{Allow: []string{"read_file", "spawn"}})
	if allowed["web_fetch"] || !allowed["spawn"] {
		t.Fatalf("policy precondition: allowed = %v", allowed)
	}

	const childTask, grandchildTask = "orchestrate", "leaf work"
	provider := newScopeScriptProvider(map[string][][]providers.ToolCall{
		childTask: {{call("1", "spawn", map[string]any{"task": grandchildTask, "mode": "sync"})}},
		grandchildTask: {{
			call("1", "web_fetch", map[string]any{"url": "https://example.invalid"}),
			call("2", "read_file", nil),
		}},
	})
	manager := newScopeManager(provider, 2)

	if _, _, _, err := manager.RunSync(parentRunContext(parent, allowed), "parent", 0, childTask, "c", "", "test", "chat"); err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if got := provider.shownTo(childTask); !slices.Equal(got, []string{"read_file", "spawn"}) {
		t.Fatalf("child was shown %v, want [read_file spawn]", got)
	}
	if res := provider.resultFor(childTask, "spawn"); !strings.Contains(res, "completed") {
		t.Fatalf("nested spawn result = %q, want completion", res)
	}
	if got := provider.shownTo(grandchildTask); !slices.Equal(got, []string{"read_file"}) {
		t.Fatalf("grandchild was shown %v, want [read_file] (leaf deny drops spawn)", got)
	}
	if webFetch.count() != 0 {
		t.Fatal("web_fetch executed in nested subagent")
	}
	if res := provider.resultFor(grandchildTask, "web_fetch"); !strings.Contains(res, "not available to this subagent") {
		t.Fatalf("grandchild web_fetch result = %q, want rejection", res)
	}
	if readFile.count() != 1 {
		t.Fatalf("grandchild read_file executed %d times, want 1", readFile.count())
	}
}

func TestSubagentToolsFailClosedWithoutParentPolicy(t *testing.T) {
	cases := []struct {
		name    string
		source  bool
		allowed bool
		factory bool
	}{
		{name: "no parent allowlist", source: true, allowed: false},
		{name: "no parent registry with parent-aware factory", source: false, allowed: true, factory: true},
		{name: "nothing published", source: false, allowed: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readFile := newScopeProbe("read_file")
			parent := NewRegistry()
			parent.Register(readFile)

			const task = "do work"
			provider := newScopeScriptProvider(map[string][][]providers.ToolCall{
				task: {{call("1", "read_file", nil)}},
			})
			// The legacy factory exposes the tool too: it must not be a fallback.
			manager := NewSubagentManager(provider, nil, "scope-model", nil, parent.Clone, SubagentConfig{
				MaxConcurrent: 8, MaxSpawnDepth: 1, MaxChildrenPerAgent: 8,
			})
			if tc.factory {
				manager.SetParentToolsFactory(func(p *Registry) *Registry { return p.Clone() })
			}
			var src *Registry
			if tc.source {
				src = parent
			}
			var allowed map[string]bool
			if tc.allowed {
				allowed = map[string]bool{"read_file": true}
			}

			if _, _, _, err := manager.RunSync(parentRunContext(src, allowed), "parent", 0, task, "t", "", "test", "chat"); err != nil {
				t.Fatalf("RunSync: %v", err)
			}
			if got := provider.shownTo(task); len(got) != 0 {
				t.Fatalf("subagent was shown %v, want no tools", got)
			}
			if readFile.count() != 0 {
				t.Fatal("tool executed although parent policy was unresolved")
			}
			if res := provider.resultFor(task, "read_file"); !strings.Contains(res, "not available to this subagent") {
				t.Fatalf("read_file result = %q, want rejection", res)
			}
		})
	}
}

// Async spawns capture the parent's boundary at spawn time; later widening of
// the parent's per-iteration map (lazy MCP activation) must not leak in.
func TestSubagentToolScopeSnapshotIsolatedFromParentMap(t *testing.T) {
	allowed := map[string]bool{"read_file": true}
	ctx := parentRunContext(NewRegistry(), allowed)
	scope := captureSubagentToolScope(ctx)
	allowed["web_fetch"] = true
	if scope.allowed["web_fetch"] {
		t.Fatal("captured scope aliases the parent's mutable allowlist")
	}
	if !scope.allowed["read_file"] || scope.source == nil {
		t.Fatalf("captured scope = %+v", scope)
	}
}
