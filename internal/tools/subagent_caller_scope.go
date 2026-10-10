package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// TaskCaller narrows a TaskScope (tenant + root agent) to the agent turn that
// issues a spawn list/wait/cancel. One root agent serves many sessions at once,
// so without it "wait" and "cancel all" would reach tasks spawned from other
// conversations of the same agent.
type TaskCaller struct {
	// SessionKey is the calling run's tool session key; tasks match on
	// OriginSessionKey (the same key spawn records for announce routing).
	SessionKey string
	// TaskID is the calling subagent's own task ID, "" for the root agent run.
	// A subagent caller only reaches tasks below itself in the spawn tree.
	TaskID string
	// RunID is the calling run's ID, "" when unknown. Used by wait only.
	RunID string
}

// TaskCallerFromContext derives the caller from tool-execution context.
func TaskCallerFromContext(ctx context.Context) TaskCaller {
	return TaskCaller{
		SessionKey: ToolSessionKeyFromCtx(ctx),
		TaskID:     subagentTaskIDFromContext(ctx),
		RunID:      runIDFromContext(ctx),
	}
}

func runIDFromContext(ctx context.Context) string {
	if rc := store.RunContextFromCtx(ctx); rc != nil {
		return rc.RunID
	}
	return ""
}

// callerOwnsLocked reports whether t was spawned from the caller's session
// and, for a subagent caller, below that subagent. Caller must hold sm.mu.
func (sm *SubagentManager) callerOwnsLocked(t *SubagentTask, caller TaskCaller) bool {
	if t.OriginSessionKey != caller.SessionKey {
		return false
	}
	if caller.TaskID == "" {
		return true
	}
	return sm.isDescendantLocked(t, caller.TaskID)
}

// isDescendantLocked walks t's ParentTaskID chain looking for ancestorID.
// A chain broken by an archived intermediate task fails closed.
func (sm *SubagentManager) isDescendantLocked(t *SubagentTask, ancestorID string) bool {
	for range 64 { // bounded: spawn depth is far smaller
		if t == nil || t.ParentTaskID == "" {
			return false
		}
		if t.ParentTaskID == ancestorID {
			return true
		}
		t = sm.tasks[t.ParentTaskID]
	}
	return false
}

// ListTasksForCaller returns snapshots of the tasks spawned from the caller's
// session (and below the caller when it is a subagent), including retained
// finished tasks.
func (sm *SubagentManager) ListTasksForCaller(scope TaskScope, caller TaskCaller) []*SubagentTask {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	var result []*SubagentTask
	for _, t := range sm.tasks {
		if taskMatchesScope(t, scope) && sm.callerOwnsLocked(t, caller) {
			result = append(result, cloneSubagentTask(t))
		}
	}
	return result
}

// CancelTaskForCaller is CancelTask with "all" and "last" limited to the
// caller's active tasks. Explicit task IDs keep the tenant/root-agent scope
// of CancelTask.
func (sm *SubagentManager) CancelTaskForCaller(scope TaskScope, caller TaskCaller, id string) bool {
	return sm.cancelTask(scope, &caller, id)
}

// waitCandidatesLocked selects the tasks a wait covers: the caller's tasks,
// only from the current run when the run ID is known, minus finished tasks
// already reported by an earlier wait. Caller must hold sm.mu.
func (sm *SubagentManager) waitCandidatesLocked(scope TaskScope, caller TaskCaller) []*SubagentTask {
	var result []*SubagentTask
	for _, t := range sm.tasks {
		if !taskMatchesScope(t, scope) || !sm.callerOwnsLocked(t, caller) {
			continue
		}
		if caller.RunID != "" && t.OriginRunID != caller.RunID {
			continue
		}
		if isTerminalTaskStatus(t.Status) && t.waitReported {
			continue
		}
		result = append(result, t)
	}
	return result
}

// waitSnapshot returns snapshots of the current wait candidates and whether
// they are all finished. When final, finished tasks are marked reported so a
// later wait does not return them again.
func (sm *SubagentManager) waitSnapshot(scope TaskScope, caller TaskCaller, final bool) ([]*SubagentTask, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	candidates := sm.waitCandidatesLocked(scope, caller)
	allDone := true
	for _, t := range candidates {
		if isActiveTaskStatus(t.Status) {
			allDone = false
			break
		}
	}
	if !final && !allDone {
		return nil, false
	}
	snapshots := make([]*SubagentTask, 0, len(candidates))
	for _, t := range candidates {
		snapshots = append(snapshots, cloneSubagentTask(t))
		if isTerminalTaskStatus(t.Status) {
			t.waitReported = true
		}
	}
	return snapshots, allDone
}

// WaitForCallerTasks blocks until the caller's wait candidates (see
// waitCandidatesLocked) are all finished, the timeout elapses, or ctx ends.
// It returns immediately when there is nothing to wait for, so a wait never
// blocks on tasks owned by another session.
func (sm *SubagentManager) WaitForCallerTasks(ctx context.Context, scope TaskScope, caller TaskCaller, timeoutSec int) ([]*SubagentTask, error) {
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	if tasks, done := sm.waitSnapshot(scope, caller, false); done {
		return tasks, nil
	}
	deadline := time.After(time.Duration(timeoutSec) * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			tasks, _ := sm.waitSnapshot(scope, caller, true)
			return tasks, ctx.Err()
		case <-deadline:
			tasks, _ := sm.waitSnapshot(scope, caller, true)
			return tasks, fmt.Errorf("timeout after %ds waiting for children", timeoutSec)
		case <-ticker.C:
			if tasks, done := sm.waitSnapshot(scope, caller, false); done {
				return tasks, nil
			}
		}
	}
}
