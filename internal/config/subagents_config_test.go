package config

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestSubagentsConfigToolDenyJSON(t *testing.T) {
	var c SubagentsConfig
	if err := json.Unmarshal([]byte(`{"maxSpawnDepth":2,"toolDeny":["mcp_*__vn_decision_*","group:fs"]}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.MaxSpawnDepth != 2 || !slices.Equal(c.ToolDeny, []string{"mcp_*__vn_decision_*", "group:fs"}) {
		t.Fatalf("decoded %+v", c)
	}
	out, _ := json.Marshal(SubagentsConfig{MaxSpawnDepth: 1})
	if string(out) != `{"maxSpawnDepth":1}` {
		t.Fatalf("empty toolDeny must be omitted, got %s", out)
	}
}

func TestValidateSubagentsConfigJSON(t *testing.T) {
	valid := []string{``, `null`, ` null `, `{}`, `{"maxSpawnDepth":2}`, `{"toolDeny":[]}`, `{"toolDeny":["exec","group:mcp:*"]}`}
	for _, raw := range valid {
		if err := ValidateSubagentsConfigJSON([]byte(raw)); err != nil {
			t.Errorf("ValidateSubagentsConfigJSON(%q) = %v, want nil", raw, err)
		}
	}
	invalid := []string{`{"toolDeny":"exec"}`, `{"toolDeny":[1]}`, `{"toolDeny":[""]}`, `{"toolDeny":["  "]}`, `{"toolDeny":["group:"]}`, `{"maxSpawnDepth":"2"}`, `[]`}
	for _, raw := range invalid {
		if err := ValidateSubagentsConfigJSON([]byte(raw)); err == nil {
			t.Errorf("ValidateSubagentsConfigJSON(%q) = nil, want error", raw)
		}
	}
}
