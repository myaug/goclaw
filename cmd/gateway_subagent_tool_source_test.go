package cmd

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type namedStubTool struct{ name string }

func (t *namedStubTool) Name() string               { return t.name }
func (t *namedStubTool) Description() string        { return t.name }
func (t *namedStubTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *namedStubTool) Execute(context.Context, map[string]any) *tools.Result {
	return tools.NewResult(t.name)
}

// The gateway's subagent factory must derive a subagent's tools from the
// spawning agent's registry (its grant-filtered MCP tools), never from the
// gateway-wide registry, and must produce nothing without a parent registry.
func TestSubagentToolsFactory_SourcesFromParentRegistry(t *testing.T) {
	factory := newSubagentToolsFactory(t.TempDir(), true, nil, nil)

	if reg := factory(nil); reg != nil {
		t.Fatalf("factory without a parent registry returned %v, want nil (fail closed)", reg.List())
	}

	granted := &namedStubTool{name: "mcp_docs__search"}
	parent := tools.NewRegistry()
	parent.Register(granted)

	reg := factory(parent)
	if reg == nil {
		t.Fatal("factory returned nil for a parent registry")
	}
	got, ok := reg.Get("mcp_docs__search")
	if !ok || got != granted {
		t.Fatalf("subagent MCP tool = %v, want the parent's instance", got)
	}
	if _, ok := reg.Get("mcp_docs__delete"); ok {
		t.Fatal("subagent registry contains a tool its parent lacks")
	}
	for _, name := range []string{"read_file", "write_file", "list_files", "exec"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("workspace-scoped %s missing; the subagent manager narrows it by parent policy", name)
		}
	}
	if _, ok := parent.Get("exec"); ok {
		t.Fatal("factory mutated the parent registry")
	}
}
