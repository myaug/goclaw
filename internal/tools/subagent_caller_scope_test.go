package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// selectiveBlockProvider finishes immediately unless the task text contains
// "block", in which case it blocks until the subagent is cancelled.
type selectiveBlockProvider struct{}

func (*selectiveBlockProvider) Name() string         { return "selective-block" }
func (*selectiveBlockProvider) DefaultModel() string { return "selective-block" }
func (*selectiveBlockProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	for _, m := range req.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "block") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return &providers.ChatResponse{Content: "done", FinishReason: "stop"}, nil
}
func (p *selectiveBlockProvider) ChatStream(ctx context.Context, req providers.ChatRequest, _ func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return p.Chat(ctx, req)
}

func sessionCtx(base context.Context, sessionKey, runID string) context.Context {
	ctx := WithToolSessionKey(base, sessionKey)
	return store.WithRunContext(ctx, &store.RunContext{RunID: runID, SessionKey: sessionKey})
}

func spawnIn(t *testing.T, m *SubagentManager, ctx context.Context, task string) string {
	t.Helper()
	receipt, err := m.SpawnWithReceipt(ctx, "parent", 0, task, task, "", "test", "chat", "direct", nil)
	if err != nil {
		t.Fatalf("spawn %q: %v", task, err)
	}
	return receipt.TaskID
}

func taskStatus(m *SubagentManager, scope TaskScope, id string) string {
	if task, ok := m.GetTask(scope, id); ok {
		return task.Status
	}
	return ""
}

func waitForStatus(t *testing.T, m *SubagentManager, scope TaskScope, id, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if taskStatus(m, scope, id) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s status = %q, want %q", id, taskStatus(m, scope, id), want)
}

// Two sessions of one agent: wait, list and cancel all in session A only
// reach A's tasks and never block on or cancel B's running task.
func TestSpawnToolActionsAreSessionScoped(t *testing.T) {
	manager := NewSubagentManager(&selectiveBlockProvider{}, nil, "model", nil, NewRegistry, SubagentConfig{
		MaxConcurrent: 8, MaxSpawnDepth: 2, MaxChildrenPerAgent: 8,
	})
	tool := NewSpawnTool(manager, "parent", 0)

	base := subagentTestContext("parent")
	scope := subagentScopeFromContext(base)
	// Unblock every task before Close (which waits for task lifecycles), even
	// when an assertion fails half-way.
	defer manager.Close()
	defer manager.CancelTask(scope, "all")
	ctxA := sessionCtx(base, "agent:parent:session-a", "run-a")
	ctxB := sessionCtx(base, "agent:parent:session-b", "run-b")

	quickA := spawnIn(t, manager, ctxA, "quick task")
	runningB := spawnIn(t, manager, ctxB, "block task")
	waitForStatus(t, manager, scope, runningB, TaskStatusRunning)

	// B's task never finishes on its own, so a wait that reports success did
	// not block on it.
	res := tool.Execute(ctxA, map[string]any{"action": "wait", "timeout": float64(30)})
	if got := taskStatus(manager, scope, runningB); got != TaskStatusRunning {
		t.Fatalf("B's task status = %q, want still running", got)
	}
	if !strings.Contains(res.ForLLM, "All 1 subagent tasks finished") || !strings.Contains(res.ForLLM, "quick task") ||
		strings.Contains(res.ForLLM, "block task") {
		t.Fatalf("wait in session A = %q, want only A's finished task", res.ForLLM)
	}

	// A second wait does not repeat the already reported result.
	res = tool.Execute(ctxA, map[string]any{"action": "wait", "timeout": float64(30)})
	if !strings.Contains(res.ForLLM, "No subagent tasks to wait for") {
		t.Fatalf("second wait in session A = %q, want nothing to wait for", res.ForLLM)
	}

	// list shows only the caller's session (retained finished tasks included).
	res = tool.Execute(ctxA, map[string]any{"action": "list"})
	if !strings.Contains(res.ForLLM, quickA) || strings.Contains(res.ForLLM, runningB) {
		t.Fatalf("list in session A = %q, want %s and not %s", res.ForLLM, quickA, runningB)
	}

	// cancel all / last in A leave B running.
	runningA := spawnIn(t, manager, ctxA, "block task in A")
	waitForStatus(t, manager, scope, runningA, TaskStatusRunning)
	if res := tool.Execute(ctxA, map[string]any{"action": "cancel", "id": "all"}); res.IsError {
		t.Fatalf("cancel all in A: %s", res.ForLLM)
	}
	if got := taskStatus(manager, scope, runningA); got != TaskStatusCancelled {
		t.Fatalf("A's task status = %q, want cancelled", got)
	}
	if got := taskStatus(manager, scope, runningB); got != TaskStatusRunning {
		t.Fatalf("B's task status after cancel all in A = %q, want running", got)
	}
	if res := tool.Execute(ctxA, map[string]any{"action": "cancel", "id": "last"}); !res.IsError {
		t.Fatalf("cancel last in A should find nothing, got %q", res.ForLLM)
	}
	if got := taskStatus(manager, scope, runningB); got != TaskStatusRunning {
		t.Fatalf("B's task status after cancel last in A = %q, want running", got)
	}

	// Session B still controls its own task.
	if res := tool.Execute(ctxB, map[string]any{"action": "cancel", "id": "last"}); res.IsError {
		t.Fatalf("cancel last in B: %s", res.ForLLM)
	}
	waitForStatus(t, manager, scope, runningB, TaskStatusCancelled)
}

func injectTask(m *SubagentManager, scope TaskScope, id, parentTaskID, sessionKey, runID, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[id] = &SubagentTask{
		ID: id, Label: id, Task: id, Status: status, ParentTaskID: parentTaskID,
		OriginSessionKey: sessionKey, OriginRunID: runID,
		OriginTenantID: scope.TenantID, RootAgentID: scope.RootAgentID, RootAgentKey: scope.RootAgentKey,
		CreatedAt: time.Now().UnixMilli(),
	}
}

func waitIDs(t *testing.T, m *SubagentManager, scope TaskScope, caller TaskCaller) []string {
	t.Helper()
	tasks, err := m.WaitForCallerTasks(context.Background(), scope, caller, 1)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	var ids []string
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func TestWaitForCallerTasksScopesToRunAndSkipsReported(t *testing.T) {
	manager := NewSubagentManager(&selectiveBlockProvider{}, nil, "model", nil, NewRegistry, SubagentConfig{})
	defer manager.Close()
	scope := TaskScope{TenantID: uuid.New(), RootAgentID: uuid.New(), RootAgentKey: "parent"}

	injectTask(manager, scope, "old-run", "", "s1", "run-1", TaskStatusCompleted)
	injectTask(manager, scope, "this-run", "", "s1", "run-2", TaskStatusCompleted)
	injectTask(manager, scope, "other-session", "", "s2", "run-2", TaskStatusRunning)
	injectTask(manager, scope, "no-run", "", "s1", "", TaskStatusCompleted)

	// Run known: only tasks spawned in this run of this session.
	if ids := waitIDs(t, manager, scope, TaskCaller{SessionKey: "s1", RunID: "run-2"}); len(ids) != 1 || ids[0] != "this-run" {
		t.Fatalf("run-scoped wait = %v, want [this-run]", ids)
	}

	// Run unknown: session tasks not yet reported by an earlier wait.
	ids := waitIDs(t, manager, scope, TaskCaller{SessionKey: "s1"})
	if len(ids) != 2 || !containsAll(ids, "old-run", "no-run") {
		t.Fatalf("session-scoped wait = %v, want old-run and no-run", ids)
	}
	if ids := waitIDs(t, manager, scope, TaskCaller{SessionKey: "s1"}); len(ids) != 0 {
		t.Fatalf("repeat wait = %v, want nothing", ids)
	}
}

// A subagent caller only reaches tasks below itself, never itself or siblings.
func TestCallerScopeForSubagentCaller(t *testing.T) {
	manager := NewSubagentManager(&selectiveBlockProvider{}, nil, "model", nil, NewRegistry, SubagentConfig{})
	defer manager.Close()
	scope := TaskScope{TenantID: uuid.New(), RootAgentID: uuid.New(), RootAgentKey: "parent"}

	injectTask(manager, scope, "child", "", "s1", "r", TaskStatusWaiting)
	injectTask(manager, scope, "sibling", "", "s1", "r", TaskStatusRunning)
	injectTask(manager, scope, "grandchild", "child", "s1", "r", TaskStatusRunning)
	injectTask(manager, scope, "great-grandchild", "grandchild", "s1", "r", TaskStatusRunning)

	caller := TaskCaller{SessionKey: "s1", TaskID: "child", RunID: "r"}
	var listed []string
	for _, task := range manager.ListTasksForCaller(scope, caller) {
		listed = append(listed, task.ID)
	}
	if len(listed) != 2 || !containsAll(listed, "grandchild", "great-grandchild") {
		t.Fatalf("subagent list = %v, want its descendants only", listed)
	}

	if !manager.CancelTaskForCaller(scope, caller, "all") {
		t.Fatal("cancel all from subagent cancelled nothing")
	}
	for id, want := range map[string]string{
		"child": TaskStatusWaiting, "sibling": TaskStatusRunning,
		"grandchild": TaskStatusCancelled, "great-grandchild": TaskStatusCancelled,
	} {
		if got := taskStatus(manager, scope, id); got != want {
			t.Errorf("%s status = %q, want %q", id, got, want)
		}
	}
}

func TestPersistedTaskInSession(t *testing.T) {
	key := func(s string) *string { return &s }
	cases := []struct {
		stored *string
		caller string
		want   bool
	}{
		{key("s1"), "s1", true},
		{key("s1"), "s2", false},
		{key("s1"), "", false},
		{nil, "s2", true},
		{key(""), "s2", true},
	}
	for _, tc := range cases {
		got := persistedTaskInSession(&store.SubagentTaskData{SessionKey: tc.stored}, tc.caller)
		if got != tc.want {
			t.Errorf("stored=%v caller=%q: got %v, want %v", tc.stored, tc.caller, got, tc.want)
		}
	}
}

func containsAll(have []string, want ...string) bool {
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}
