package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// Subagents spawned during a run source their tools from the running agent's
// own registry. injectContext must publish it, replacing any registry an outer
// agent's run left in the context.
func TestInjectContext_PublishesOwnRegistryAsSubagentToolSource(t *testing.T) {
	own := tools.NewRegistry()
	loop := NewLoop(LoopConfig{
		ID:        "worker",
		AgentUUID: uuid.New(),
		TenantID:  store.MasterTenantID,
		Workspace: t.TempDir(),
		Sessions:  &nopSessionStore{},
		Tools:     own,
	})

	outer := tools.WithSubagentToolSource(context.Background(), tools.NewRegistry())
	setup, err := loop.injectContext(outer, &RunRequest{SessionKey: "agent:worker:test"})
	if err != nil {
		t.Fatalf("injectContext: %v", err)
	}
	if got := tools.SubagentToolSourceFromCtx(setup.ctx); got != own {
		t.Fatalf("subagent tool source = %p, want the loop's own registry %p", got, own)
	}
}
