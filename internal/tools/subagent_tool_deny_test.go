package tools

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// mcpScopeProbe stands in for an MCP bridge tool: registered under its prefixed
// name, exposing the server's original tool name.
type mcpScopeProbe struct {
	*scopeProbeTool
	original string
}

func newMCPScopeProbe(prefix, original string) *mcpScopeProbe {
	return &mcpScopeProbe{scopeProbeTool: newScopeProbe(prefix + "__" + original), original: original}
}

func (t *mcpScopeProbe) OriginalName() string { return t.original }

// denyCtxProbe records the toolDeny patterns published to the context it runs in.
type denyCtxProbe struct {
	mu   sync.Mutex
	seen [][]string
}

func (t *denyCtxProbe) Name() string               { return "deny_probe" }
func (t *denyCtxProbe) Description() string        { return "records inherited toolDeny" }
func (t *denyCtxProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *denyCtxProbe) Execute(ctx context.Context, _ map[string]any) *Result {
	t.mu.Lock()
	t.seen = append(t.seen, subagentToolDenyFromCtx(ctx))
	t.mu.Unlock()
	return NewResult("ok")
}

func withToolDeny(ctx context.Context, patterns ...string) context.Context {
	return WithSubagentConfig(ctx, &config.SubagentsConfig{ToolDeny: patterns})
}

// A parent holding MCP write tools spawns a read-only scout: the denied tools
// are neither shown nor executable, the read tools still work.
func TestSubagentToolDenyRemovesMCPWriteTools(t *testing.T) {
	propose := newMCPScopeProbe("mcp_vn", "vn_decision_propose")
	holdings := newMCPScopeProbe("mcp_vn", "vn_portfolio_set_holdings")
	quote := newMCPScopeProbe("mcp_vn", "vn_market_quote")
	readFile := newScopeProbe("read_file")
	parent := NewRegistry()
	for _, tool := range []Tool{propose, holdings, quote, readFile} {
		parent.Register(tool)
	}
	parent.RegisterToolGroup("mcp:vn", []string{propose.Name(), holdings.Name(), quote.Name()})
	allowed := parentAllowedTools(parent, nil)
	if !allowed[propose.Name()] || !allowed[holdings.Name()] {
		t.Fatalf("policy precondition: parent must hold the write tools, allowed = %v", allowed)
	}

	cases := []struct {
		name string
		deny []string
	}{
		{name: "prefixed glob", deny: []string{"mcp_*__vn_decision_*", "mcp_vn__vn_portfolio_set_*"}},
		{name: "bare MCP name glob", deny: []string{"vn_decision_*", "vn_portfolio_set_holdings"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := map[Tool]int{propose: propose.count(), holdings: holdings.count(), quote: quote.count(), readFile: readFile.count()}
			task := "scout " + tc.name
			provider := newScopeScriptProvider(map[string][][]providers.ToolCall{
				task: {{
					call("1", propose.Name(), nil),
					call("2", holdings.Name(), nil),
					call("3", quote.Name(), nil),
					call("4", "read_file", nil),
				}},
			})
			manager := newScopeManager(provider, 1)
			ctx := withToolDeny(parentRunContext(parent, allowed), tc.deny...)
			if _, _, _, err := manager.RunSync(ctx, "parent", 0, task, "t", "", "test", "chat"); err != nil {
				t.Fatalf("RunSync: %v", err)
			}

			if got, want := provider.shownTo(task), []string{quote.Name(), "read_file"}; !slices.Equal(got, want) {
				t.Fatalf("subagent was shown %v, want %v", got, want)
			}
			for _, denied := range []*mcpScopeProbe{propose, holdings} {
				if denied.count() != before[denied] {
					t.Fatalf("%s executed in subagent despite toolDeny", denied.Name())
				}
				if res := provider.resultFor(task, denied.Name()); !strings.Contains(res, "not available to this subagent") {
					t.Fatalf("%s result = %q, want rejection", denied.Name(), res)
				}
			}
			if quote.count() != before[quote]+1 || readFile.count() != before[readFile]+1 {
				t.Fatal("read tools must still run in the subagent")
			}
		})
	}

	// The parent itself is untouched: toolDeny only applies to its children.
	if _, ok := parent.Get(propose.Name()); !ok {
		t.Fatal("toolDeny removed the tool from the parent's own registry")
	}
}

// "group:" entries expand against the child's tool groups; the group name may
// be a glob. Alias names are matched as shown to the model.
func TestSubagentToolDenyGroupsAndGlobs(t *testing.T) {
	newParent := func() *Registry {
		reg := NewRegistry()
		for _, name := range []string{"read_file", "write_file", "web_fetch", "web_search", "exec", "mcp_vn__vn_quote", "mcp_docs__search"} {
			reg.Register(newScopeProbe(name))
		}
		reg.RegisterAlias("bash", "exec")
		reg.RegisterToolGroup("mcp:vn", []string{"mcp_vn__vn_quote"})
		reg.RegisterToolGroup("mcp:docs", []string{"mcp_docs__search"})
		return reg
	}
	cases := []struct {
		name string
		deny []string
		want []string
	}{
		{name: "builtin group", deny: []string{"group:fs"},
			want: []string{"bash", "exec", "mcp_docs__search", "mcp_vn__vn_quote", "web_fetch", "web_search"}},
		{name: "glob over group names", deny: []string{"group:mcp:*"},
			want: []string{"bash", "exec", "read_file", "web_fetch", "web_search", "write_file"}},
		{name: "one MCP server group", deny: []string{"group:mcp:vn"},
			want: []string{"bash", "exec", "mcp_docs__search", "read_file", "web_fetch", "web_search", "write_file"}},
		{name: "name glob", deny: []string{"web_*", "*_file"},
			want: []string{"bash", "exec", "mcp_docs__search", "mcp_vn__vn_quote"}},
		{name: "canonical tool takes its alias", deny: []string{"exec"},
			want: []string{"mcp_docs__search", "mcp_vn__vn_quote", "read_file", "web_fetch", "web_search", "write_file"}},
		{name: "alias name only", deny: []string{"bash"},
			want: []string{"exec", "mcp_docs__search", "mcp_vn__vn_quote", "read_file", "web_fetch", "web_search", "write_file"}},
		{name: "unknown group is a no-op", deny: []string{"group:nope*"},
			want: []string{"bash", "exec", "mcp_docs__search", "mcp_vn__vn_quote", "read_file", "web_fetch", "web_search", "write_file"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := newParent()
			allowed := parentAllowedTools(parent, nil)
			task := "work " + tc.name
			provider := newScopeScriptProvider(map[string][][]providers.ToolCall{
				task: {{call("1", "bash", map[string]any{"command": "id"})}},
			})
			manager := newScopeManager(provider, 1)
			ctx := withToolDeny(parentRunContext(parent, allowed), tc.deny...)
			if _, _, _, err := manager.RunSync(ctx, "parent", 0, task, "t", "", "test", "chat"); err != nil {
				t.Fatalf("RunSync: %v", err)
			}
			if got := provider.shownTo(task); !slices.Equal(got, tc.want) {
				t.Fatalf("subagent was shown %v, want %v", got, tc.want)
			}
			res := provider.resultFor(task, "bash")
			if shown := slices.Contains(tc.want, "bash"); shown == strings.Contains(res, "not available to this subagent") {
				t.Fatalf("bash shown=%v but result = %q", shown, res)
			}
		})
	}
}

// A subagent publishes its toolDeny; the subagents it spawns keep it even when
// their own agent config would not deny anything, and add their own config.
func TestNestedSubagentInheritsToolDeny(t *testing.T) {
	propose := newMCPScopeProbe("mcp_vn", "vn_decision_propose")
	quote := newMCPScopeProbe("mcp_vn", "vn_market_quote")
	probe := &denyCtxProbe{}
	parent := NewRegistry()
	parent.Register(propose)
	parent.Register(quote)
	parent.Register(probe)
	parent.Register(newScopeProbe("spawn")) // replaced per subagent
	allowed := parentAllowedTools(parent, nil)

	const childTask, grandchildTask = "orchestrate scouts", "scout leaf"
	provider := newScopeScriptProvider(map[string][][]providers.ToolCall{
		childTask: {{call("1", "deny_probe", nil), call("2", "spawn", map[string]any{"task": grandchildTask, "mode": "sync"})}},
		grandchildTask: {{
			call("1", propose.Name(), nil),
			call("2", quote.Name(), nil),
			call("3", "deny_probe", nil),
		}},
	})
	manager := newScopeManager(provider, 2)
	ctx := withToolDeny(parentRunContext(parent, allowed), "vn_decision_*")
	if _, _, _, err := manager.RunSync(ctx, "parent", 0, childTask, "c", "", "test", "chat"); err != nil {
		t.Fatalf("RunSync: %v", err)
	}

	if got, want := provider.shownTo(childTask), []string{"deny_probe", quote.Name(), "spawn"}; !slices.Equal(got, want) {
		t.Fatalf("child was shown %v, want %v", got, want)
	}
	if got, want := provider.shownTo(grandchildTask), []string{"deny_probe", quote.Name()}; !slices.Equal(got, want) {
		t.Fatalf("grandchild was shown %v, want %v", got, want)
	}
	if propose.count() != 0 {
		t.Fatal("denied MCP tool executed in nested subagent")
	}
	if res := provider.resultFor(grandchildTask, propose.Name()); !strings.Contains(res, "not available to this subagent") {
		t.Fatalf("grandchild %s result = %q, want rejection", propose.Name(), res)
	}
	if quote.count() != 1 {
		t.Fatalf("grandchild read tool ran %d times, want 1", quote.count())
	}
	probe.mu.Lock()
	seen := probe.seen
	probe.mu.Unlock()
	if len(seen) != 2 || !slices.Equal(seen[0], []string{"vn_decision_*"}) || !slices.Equal(seen[1], []string{"vn_decision_*"}) {
		t.Fatalf("published toolDeny per level = %v, want [vn_decision_*] at both depths", seen)
	}

	// The captured list is the union of what the caller inherited and the
	// spawning agent's own config, without duplicates.
	inherited := withSubagentToolDeny(context.Background(), []string{"vn_decision_*", "exec"})
	scope := captureSubagentToolScope(inherited, []string{"exec", " web_* ", ""})
	if want := []string{"vn_decision_*", "exec", "web_*"}; !slices.Equal(scope.deny, want) {
		t.Fatalf("captured deny = %v, want %v", scope.deny, want)
	}
}

// No list, an empty list, or blank entries leave the child's tools unchanged.
func TestSubagentToolDenyEmptyIsNoChange(t *testing.T) {
	parent := NewRegistry()
	for _, name := range []string{"read_file", "web_fetch", "mcp_vn__vn_decision_propose"} {
		parent.Register(newScopeProbe(name))
	}
	allowed := parentAllowedTools(parent, nil)
	want := []string{"mcp_vn__vn_decision_propose", "read_file", "web_fetch"}

	cases := []struct {
		name string
		cfg  *config.SubagentsConfig
	}{
		{name: "no subagents config"},
		{name: "nil toolDeny", cfg: &config.SubagentsConfig{MaxSpawnDepth: 1}},
		{name: "empty toolDeny", cfg: &config.SubagentsConfig{ToolDeny: []string{}}},
		{name: "blank entries", cfg: &config.SubagentsConfig{ToolDeny: []string{"", "  ", "group:"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := "plain " + tc.name
			provider := newScopeScriptProvider(nil)
			manager := newScopeManager(provider, 1)
			ctx := parentRunContext(parent, allowed)
			if tc.cfg != nil {
				ctx = WithSubagentConfig(ctx, tc.cfg)
			}
			if _, _, _, err := manager.RunSync(ctx, "parent", 0, task, "t", "", "test", "chat"); err != nil {
				t.Fatalf("RunSync: %v", err)
			}
			if got := provider.shownTo(task); !slices.Equal(got, want) {
				t.Fatalf("subagent was shown %v, want %v", got, want)
			}
		})
	}
}

// A gateway default toolDeny is kept when an agent configures its own list.
func TestSubagentToolDenyPerAgentAddsToDefault(t *testing.T) {
	manager := NewSubagentManager(nil, nil, "m", nil, nil, SubagentConfig{MaxSpawnDepth: 1, ToolDeny: []string{"exec"}})
	if got := manager.effectiveConfig(context.Background()).ToolDeny; !slices.Equal(got, []string{"exec"}) {
		t.Fatalf("default toolDeny = %v", got)
	}
	ctx := withToolDeny(context.Background(), "web_*", "exec")
	if got := manager.effectiveConfig(ctx).ToolDeny; !slices.Equal(got, []string{"exec", "web_*"}) {
		t.Fatalf("merged toolDeny = %v, want [exec web_*]", got)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"exec", "exec", true},
		{"exec", "exec2", false},
		{"*", "anything", true},
		{"web_*", "web_fetch", true},
		{"web_*", "web_", true},
		{"web_*", "xweb_fetch", false},
		{"*_file", "read_file", true},
		{"mcp_*__vn_decision_*", "mcp_vn__vn_decision_propose", true},
		{"mcp_*__vn_decision_*", "mcp_vn__vn_portfolio_set_holdings", false},
		{"mcp_*__vn_decision_*", "vn_decision_propose", false},
		{"a*a", "a", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "acb", false},
		{"web_?", "web_x", false}, // only "*" is special
		{"[a]", "a", false},
	}
	for _, tc := range cases {
		if got := globMatch(tc.pattern, tc.name); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}
