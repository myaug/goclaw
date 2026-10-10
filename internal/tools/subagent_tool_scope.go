package tools

import (
	"context"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Subagent tool scoping.
//
// A spawned subagent must never be able to call a tool its parent could not
// call. Two values published by the parent's run define that boundary, and
// both are captured when the subagent is created:
//
//   - the parent's effective tool set: the per-iteration allowlist the agent
//     pipeline publishes via store.WithAvailableToolNames after the tool policy
//     ran (global/agent allow, deny and alsoAllow with group expansion and
//     aliases, tenant exclusions, orchestration mode, channel visibility);
//   - the parent's own tool registry (WithSubagentToolSource): the per-agent
//     registry holding the MCP tools that survived the agent's grant filtering,
//     each carrying the same execution-time grant re-check the parent uses.
//
// The child registry is built from the parent's registry, cut down to the
// parent's allowlist, then narrowed by the subagent deny lists. The resulting
// name set is both what the model is shown and what it may execute. Nested
// spawns inherit the child's set, so restrictions only ever narrow.
//
// If either value is missing the parent's policy cannot be established, and the
// subagent runs with no tools at all rather than with the full gateway registry.

const ctxSubagentToolSource toolContextKey = "subagent_tool_source"

// WithSubagentToolSource publishes the registry the calling agent executes
// tools against, so subagents it spawns source their tools from it rather than
// from the gateway-wide registry.
func WithSubagentToolSource(ctx context.Context, reg *Registry) context.Context {
	return context.WithValue(ctx, ctxSubagentToolSource, reg)
}

// SubagentToolSourceFromCtx returns the registry published by
// WithSubagentToolSource, or nil when none was set.
func SubagentToolSourceFromCtx(ctx context.Context) *Registry {
	reg, _ := ctx.Value(ctxSubagentToolSource).(*Registry)
	return reg
}

// SubagentToolsFactory builds the registry a subagent starts from, given the
// registry of the agent (or subagent) that spawned it. Implementations may
// replace tools with subagent-specific variants (e.g. workspace-scoped file and
// exec tools), but must not add tools the parent lacks: anything outside the
// parent's allowlist is removed afterwards regardless.
type SubagentToolsFactory func(parent *Registry) *Registry

// SetParentToolsFactory installs the factory used to derive a subagent's
// registry from its parent's registry. When unset, the parent registry is
// cloned as is.
func (sm *SubagentManager) SetParentToolsFactory(f SubagentToolsFactory) {
	sm.parentToolsFactory = f
}

// subagentToolScope is the parent's tool boundary captured at spawn time.
type subagentToolScope struct {
	allowed map[string]bool // parent's effective tool names; nil = unresolved
	source  *Registry       // parent's registry; nil = unknown
}

// captureSubagentToolScope snapshots the caller's tool boundary. The allowlist
// is copied because the pipeline may extend its own map later in the run (lazy
// MCP activation), which must not widen an already spawned child.
func captureSubagentToolScope(ctx context.Context) subagentToolScope {
	scope := subagentToolScope{source: SubagentToolSourceFromCtx(ctx)}
	if names := store.AvailableToolNamesFromContext(ctx); names != nil {
		scope.allowed = make(map[string]bool, len(names))
		for name, ok := range names {
			if ok {
				scope.allowed[name] = true
			}
		}
	}
	return scope
}

// buildSubagentTools returns the subagent's registry, the tool definitions sent
// to its model, and the execution allowlist (exactly the names in those
// definitions). It never falls back to the gateway-wide registry.
func (sm *SubagentManager) buildSubagentTools(task *SubagentTask) (*Registry, []providers.ToolDefinition, map[string]bool) {
	scope := task.toolScope
	failClosed := func(reason string) (*Registry, []providers.ToolDefinition, map[string]bool) {
		slog.Warn("security.subagent_tools_fail_closed",
			"id", task.ID,
			"root_agent", task.RootAgentKey,
			"depth", task.Depth,
			"reason", reason)
		return NewRegistry(), nil, map[string]bool{}
	}

	if scope.allowed == nil {
		return failClosed("parent tool policy unresolved")
	}

	var reg *Registry
	switch {
	case scope.source != nil && sm.parentToolsFactory != nil:
		reg = sm.parentToolsFactory(scope.source)
	case scope.source != nil:
		reg = scope.source.Clone()
	case sm.parentToolsFactory == nil && sm.createTools != nil:
		// Standalone managers without a parent-aware factory (tests, embedders)
		// keep their legacy base registry; it is still cut to the allowlist.
		reg = sm.createTools()
	}
	if reg == nil {
		return failClosed("parent tool registry unavailable")
	}

	reg.Register(NewSpawnTool(sm, task.RootAgentKey, task.Depth))

	// Parent boundary: drop every tool the parent could not call.
	for _, name := range reg.List() {
		if !scope.allowed[name] {
			reg.Unregister(name)
		}
	}
	sm.applyDenyList(reg, task.Depth, task.spawnConfig)

	// Aliases are emitted for surviving canonical tools; keep only the names the
	// parent was itself shown, so an alias cannot reach past the boundary either.
	all := reg.ProviderDefs()
	defs := make([]providers.ToolDefinition, 0, len(all))
	allowed := make(map[string]bool, len(all))
	for _, td := range all {
		if td.Function == nil || !scope.allowed[td.Function.Name] {
			continue
		}
		defs = append(defs, td)
		allowed[td.Function.Name] = true
	}
	return reg, defs, allowed
}
