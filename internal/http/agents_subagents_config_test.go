package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// subagentsConfigAgentStore adds get-by-id and update to the create/list fake,
// persisting JSON columns the way the SQL stores do (marshalled value).
type subagentsConfigAgentStore struct {
	gatewayOperatorAgentStore
}

func (s *subagentsConfigAgentStore) GetByID(_ context.Context, id uuid.UUID) (*store.AgentData, error) {
	for i := range s.agents {
		if s.agents[i].ID == id {
			cp := s.agents[i]
			return &cp, nil
		}
	}
	return nil, errors.New("not found")
}

func (s *subagentsConfigAgentStore) Update(_ context.Context, id uuid.UUID, updates map[string]any) error {
	for i := range s.agents {
		if s.agents[i].ID != id {
			continue
		}
		if v, ok := updates["subagents_config"]; ok {
			raw, err := json.Marshal(v)
			if err != nil {
				return err
			}
			s.agents[i].SubagentsConfig = raw
		}
		return nil
	}
	return errors.New("not found")
}

func subagentsConfigHandler() (*AgentsHandler, *subagentsConfigAgentStore) {
	agents := &subagentsConfigAgentStore{}
	return &AgentsHandler{
		agents:           agents,
		defaultWorkspace: "/tmp/workspace",
		isOwner:          func(string) bool { return true },
	}, agents
}

func serveAgents(t *testing.T, h *AgentsHandler, method, path, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req = req.WithContext(gatewayOperatorContext())
	if id != "" {
		req.SetPathValue("id", id)
	}
	rr := httptest.NewRecorder()
	switch method {
	case http.MethodPost:
		h.handleCreate(rr, req)
	case http.MethodPut:
		h.handleUpdate(rr, req)
	default:
		h.handleGet(rr, req)
	}
	return rr
}

func decodeSubagentsConfig(t *testing.T, body []byte) (uuid.UUID, map[string]any) {
	t.Helper()
	var resp struct {
		ID              uuid.UUID      `json:"id"`
		SubagentsConfig map[string]any `json:"subagents_config"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	return resp.ID, resp.SubagentsConfig
}

func toolDenyOf(t *testing.T, cfg map[string]any) []string {
	t.Helper()
	raw, _ := cfg["toolDeny"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("toolDeny entry %v is not a string", v)
		}
		out = append(out, s)
	}
	return out
}

func TestAgentsAPISubagentsToolDenyRoundTrip(t *testing.T) {
	h, agents := subagentsConfigHandler()

	rr := serveAgents(t, h, http.MethodPost, "/v1/agents", "", `{
		"agent_key":"market-analyst",
		"display_name":"Analyst",
		"provider":"anthropic",
		"model":"claude",
		"summon":false,
		"subagents_config":{"maxSpawnDepth":2,"toolDeny":["mcp_*__vn_decision_*","group:fs"]}
	}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	id, cfg := decodeSubagentsConfig(t, rr.Body.Bytes())
	if got, want := toolDenyOf(t, cfg), []string{"mcp_*__vn_decision_*", "group:fs"}; !slices.Equal(got, want) {
		t.Fatalf("create response toolDeny = %v, want %v", got, want)
	}
	if cfg["maxSpawnDepth"] != float64(2) {
		t.Fatalf("create response subagents_config = %v", cfg)
	}
	parsed := agents.agents[0].ParseSubagentsConfig()
	if parsed == nil || !slices.Equal(parsed.ToolDeny, []string{"mcp_*__vn_decision_*", "group:fs"}) || parsed.MaxSpawnDepth != 2 {
		t.Fatalf("stored subagents config = %+v", parsed)
	}

	rr = serveAgents(t, h, http.MethodPut, "/v1/agents/"+id.String(), id.String(),
		`{"subagents_config":{"maxSpawnDepth":2,"toolDeny":["vn_portfolio_set_*"]}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = serveAgents(t, h, http.MethodGet, "/v1/agents/"+id.String(), id.String(), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, cfg = decodeSubagentsConfig(t, rr.Body.Bytes()); !slices.Equal(toolDenyOf(t, cfg), []string{"vn_portfolio_set_*"}) {
		t.Fatalf("get after update subagents_config = %v", cfg)
	}
	if parsed := agents.agents[0].ParseSubagentsConfig(); parsed == nil || !slices.Equal(parsed.ToolDeny, []string{"vn_portfolio_set_*"}) {
		t.Fatalf("stored subagents config after update = %+v", parsed)
	}
}

// A subagents_config that does not decode would be ignored at runtime, dropping
// its toolDeny silently; the API rejects it instead.
func TestAgentsAPIRejectsInvalidSubagentsToolDeny(t *testing.T) {
	h, agents := subagentsConfigHandler()
	for _, cfg := range []string{`{"toolDeny":"mcp_*"}`, `{"toolDeny":[""]}`, `{"toolDeny":[1]}`} {
		rr := serveAgents(t, h, http.MethodPost, "/v1/agents", "",
			`{"agent_key":"bad-cfg","provider":"anthropic","model":"claude","summon":false,"subagents_config":`+cfg+`}`)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("create with %s: status=%d body=%s, want 400", cfg, rr.Code, rr.Body.String())
		}
	}
	if len(agents.agents) != 0 {
		t.Fatalf("invalid create persisted %d agents", len(agents.agents))
	}

	rr := serveAgents(t, h, http.MethodPost, "/v1/agents", "",
		`{"agent_key":"ok-cfg","provider":"anthropic","model":"claude","summon":false,"subagents_config":{"toolDeny":["exec"]}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	id, _ := decodeSubagentsConfig(t, rr.Body.Bytes())
	rr = serveAgents(t, h, http.MethodPut, "/v1/agents/"+id.String(), id.String(), `{"subagents_config":{"toolDeny":"exec"}}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("update status=%d body=%s, want 400", rr.Code, rr.Body.String())
	}
	if parsed := agents.agents[0].ParseSubagentsConfig(); parsed == nil || !slices.Equal(parsed.ToolDeny, []string{"exec"}) {
		t.Fatalf("rejected update changed stored config: %+v", parsed)
	}
	// JSON null still clears the override.
	rr = serveAgents(t, h, http.MethodPut, "/v1/agents/"+id.String(), id.String(), `{"subagents_config":null}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", rr.Code, rr.Body.String())
	}
}
