package tools

import (
	"context"
	"slices"
	"strings"
)

// Per-agent subagent tool deny list (subagents_config.toolDeny).
//
// An agent may hold tools that its spawned subagents must not have, e.g. MCP
// write tools, so that subagents act as read-only scouts. The list is applied
// after the parent boundary and the built-in subagent deny lists, to both the
// tool definitions and the execution allowlist. Each entry is one of:
//
//   - a tool name, matched exactly ("write_file");
//   - a glob where "*" matches any run of characters, including none
//     ("mcp_*__*_delete", "web_*"); no other character is special;
//   - "group:<name>", the members of a tool group ("group:fs", "group:mcp:vn").
//     The group name may itself be a glob ("group:mcp:*").
//
// Names and globs are matched against the name the subagent sees (for MCP
// tools the prefixed "mcp_<prefix>__<tool>") and, for tools that expose one,
// against the original MCP tool name, so "orders_*" also denies
// "mcp_shop__orders_create". Matching is case-sensitive.
//
// A subagent publishes its patterns to its tool context; any subagent it spawns
// adds them to its own agent's list, so the list only ever grows down the tree.

const ctxSubagentToolDeny toolContextKey = "subagent_tool_deny"

func withSubagentToolDeny(ctx context.Context, patterns []string) context.Context {
	if len(patterns) == 0 {
		return ctx
	}
	return context.WithValue(ctx, ctxSubagentToolDeny, slices.Clone(patterns))
}

func subagentToolDenyFromCtx(ctx context.Context) []string {
	patterns, _ := ctx.Value(ctxSubagentToolDeny).([]string)
	return patterns
}

// mergeToolDeny returns the trimmed, de-duplicated union of the given pattern
// lists, preserving first-seen order. Blank entries are dropped.
func mergeToolDeny(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range lists {
		for _, p := range list {
			p = strings.TrimSpace(p)
			if p == "" || p == "group:" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// originalToolNamer is implemented by tools registered under a different name
// than their source's, such as MCP bridge tools.
type originalToolNamer interface {
	OriginalName() string
}

// toolDenyMatcher is a toolDeny list resolved against one registry's groups.
type toolDenyMatcher struct {
	globs   []string        // name patterns, exact or with "*"
	members map[string]bool // names expanded from "group:" entries
}

func newToolDenyMatcher(reg *Registry, patterns []string) toolDenyMatcher {
	m := toolDenyMatcher{members: map[string]bool{}}
	var groupGlobs []string
	for _, p := range patterns {
		if g, ok := strings.CutPrefix(p, "group:"); ok {
			groupGlobs = append(groupGlobs, g)
		} else {
			m.globs = append(m.globs, p)
		}
	}
	if len(groupGlobs) == 0 || reg == nil {
		return m
	}
	reg.toolGroupsMu.RLock()
	defer reg.toolGroupsMu.RUnlock()
	for name, group := range reg.toolGroups {
		for _, g := range groupGlobs {
			if globMatch(g, name) {
				for _, member := range group {
					m.members[member] = true
				}
				break
			}
		}
	}
	return m
}

func (m toolDenyMatcher) empty() bool { return len(m.globs) == 0 && len(m.members) == 0 }

// matches reports whether a tool is denied, by its visible name or, when
// non-empty, its original name.
func (m toolDenyMatcher) matches(name, original string) bool {
	if m.members[name] {
		return true
	}
	for _, g := range m.globs {
		if globMatch(g, name) || (original != "" && globMatch(g, original)) {
			return true
		}
	}
	return false
}

// unregisterFrom removes every denied tool (disabled ones included) from reg and
// returns the removed names.
func (m toolDenyMatcher) unregisterFrom(reg *Registry) []string {
	if m.empty() {
		return nil
	}
	return reg.UnregisterMatching(func(t Tool) bool {
		original := ""
		if o, ok := t.(originalToolNamer); ok {
			original = o.OriginalName()
		}
		return m.matches(t.Name(), original)
	})
}

// globMatch matches name against pattern, where "*" matches any run of
// characters and every other character matches itself.
func globMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	name = name[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(name, part)
		if i < 0 {
			return false
		}
		name = name[i+len(part):]
	}
	return strings.HasSuffix(name, last)
}
