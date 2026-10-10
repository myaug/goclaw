package mcp

import (
	"fmt"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func TestSetInlineToolLimit(t *testing.T) {
	t.Cleanup(func() { SetInlineToolLimit(0) })

	cases := []struct {
		in, want int
	}{
		{0, DefaultInlineToolLimit},
		{1, 1},
		{75, 75},
		{MaxInlineToolLimit, MaxInlineToolLimit},
		{MaxInlineToolLimit + 1, DefaultInlineToolLimit},
		{-3, DefaultInlineToolLimit},
	}
	for _, tc := range cases {
		SetInlineToolLimit(75) // non-default starting point: invalid input must reset, not keep
		SetInlineToolLimit(tc.in)
		if got := InlineToolLimit(); got != tc.want {
			t.Errorf("SetInlineToolLimit(%d): InlineToolLimit() = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSearchMode_UsesConfiguredInlineLimit(t *testing.T) {
	t.Cleanup(func() { SetInlineToolLimit(0) })
	SetInlineToolLimit(5)

	m, _ := setupOverLimitManager(t, 8)
	m.mu.RLock()
	inline, deferred := len(m.inlineToolNames), len(m.deferredTools)
	m.mu.RUnlock()
	if inline != 5 || deferred != 3 {
		t.Fatalf("inline=%d deferred=%d, want 5 and 3", inline, deferred)
	}

	// At the limit there is no search mode.
	SetInlineToolLimit(8)
	reg := tools.NewRegistry()
	m2 := NewManager(reg)
	var names []string
	for i := range 8 {
		bt := makeBridgeTool("svc", fmt.Sprintf("t%d", i))
		reg.Register(bt)
		names = append(names, bt.Name())
	}
	m2.servers["svc"] = &serverState{name: "svc", toolNames: names}
	m2.maybeEnterSearchMode()
	if m2.IsSearchMode() {
		t.Fatal("8 tools with limit 8 should stay inline")
	}
}
