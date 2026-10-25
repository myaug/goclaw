package agent

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

func TestCountLLMCalls(t *testing.T) {
	calls := []providers.CallUsage{
		{Type: "llm_call", CostUSD: 0.01},
		{Type: "tool_call"},
		{Type: "llm_call", CostUSD: 0.02},
	}
	if got := countLLMCalls(calls); got != 2 {
		t.Fatalf("countLLMCalls = %d, want 2", got)
	}
	if got := countLLMCalls(nil); got != 0 {
		t.Fatalf("countLLMCalls(nil) = %d, want 0", got)
	}
	if got := providers.SumCallCost(calls); got < 0.0299 || got > 0.0301 {
		t.Fatalf("SumCallCost = %v, want 0.03", got)
	}
}
