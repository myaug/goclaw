package mcp

import (
	"log/slog"
	"sync/atomic"
)

const (
	// DefaultInlineToolLimit is the number of MCP tools an agent gets
	// registered inline before the manager switches to search mode
	// (excess tools deferred and discovered via mcp_tool_search).
	DefaultInlineToolLimit = 40
	// MaxInlineToolLimit bounds the configurable limit so a typo cannot
	// put hundreds of tool schemas into every request.
	MaxInlineToolLimit = 200
)

// inlineToolLimit holds the active limit; 0 means "use the default".
var inlineToolLimit atomic.Int32

// SetInlineToolLimit configures the inline MCP tool limit for managers that
// enter search mode afterwards. Call once at gateway startup.
// 0 keeps the default; values outside 1..MaxInlineToolLimit are ignored with
// a warning and the default is used.
func SetInlineToolLimit(n int) {
	switch {
	case n == 0:
		inlineToolLimit.Store(0)
	case n < 0 || n > MaxInlineToolLimit:
		slog.Warn("mcp.inline_tool_limit.invalid",
			"value", n, "min", 1, "max", MaxInlineToolLimit, "using", DefaultInlineToolLimit)
		inlineToolLimit.Store(0)
	default:
		inlineToolLimit.Store(int32(n))
		if n != DefaultInlineToolLimit {
			slog.Info("mcp.inline_tool_limit.configured", "value", n)
		}
	}
}

// InlineToolLimit returns the active inline MCP tool limit.
func InlineToolLimit() int {
	if n := inlineToolLimit.Load(); n > 0 {
		return int(n)
	}
	return DefaultInlineToolLimit
}
