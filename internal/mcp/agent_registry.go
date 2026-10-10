package mcp

import (
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// CloneForAgent returns a copy of a shared (gateway-wide) tool registry with
// every MCP bridge tool removed, as the starting point for one agent's
// registry. The agent's own Manager then registers the MCP tools its grants
// allow (tool_allow/tool_deny applied up front, grant re-checked on every call),
// so those are the only MCP tools the agent can see or execute.
//
// The gateway keeps its own MCP manager on a private registry, so the shared
// registry is expected to hold no bridge tools at all. This strip enforces the
// invariant where agent registries are built: a bridge tool reaching the
// shared registry (from the gateway manager or anywhere else) would otherwise
// be cloned into every agent, ungranted and without a grant check, and would
// also shadow the agent's own grant-checked tool of the same name, because
// registration skips names that already exist.
func CloneForAgent(shared *tools.Registry) *tools.Registry {
	reg := shared.Clone()
	removed := reg.UnregisterMatching(func(t tools.Tool) bool {
		_, isBridge := t.(*BridgeTool)
		return isBridge
	})
	if len(removed) > 0 {
		reg.UnregisterToolGroup("mcp")
		slog.Warn("security.mcp_shared_registry_bridge_tools_stripped",
			"count", len(removed), "tools", removed)
	}
	return reg
}
