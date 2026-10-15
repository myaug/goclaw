package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// tenantProviderStore is a tenant-scoped ProviderStore fake: reads only see rows
// whose TenantID matches the tenant in ctx, mirroring the PG scopeClause.
type tenantProviderStore struct {
	*mockProviderStore
	rows []*store.LLMProviderData
}

func (s *tenantProviderStore) GetProvider(ctx context.Context, id uuid.UUID) (*store.LLMProviderData, error) {
	tid := store.TenantIDFromContext(ctx)
	for _, p := range s.rows {
		if p.ID == id && p.TenantID == tid {
			clone := *p
			return &clone, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (s *tenantProviderStore) GetProviderByName(ctx context.Context, name string) (*store.LLMProviderData, error) {
	tid := store.TenantIDFromContext(ctx)
	for _, p := range s.rows {
		if p.Name == name && p.TenantID == tid {
			clone := *p
			return &clone, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (s *tenantProviderStore) ListProviders(ctx context.Context) ([]store.LLMProviderData, error) {
	tid := store.TenantIDFromContext(ctx)
	var out []store.LLMProviderData
	for _, p := range s.rows {
		if p.TenantID == tid {
			out = append(out, *p)
		}
	}
	return out, nil
}

func newInheritedFixture() (*tenantProviderStore, uuid.UUID, map[string]*store.LLMProviderData) {
	tenantID := uuid.New()
	mk := func(tid uuid.UUID, name string, enabled bool) *store.LLMProviderData {
		return &store.LLMProviderData{
			BaseModel:    store.BaseModel{ID: uuid.New()},
			TenantID:     tid,
			Name:         name,
			ProviderType: store.ProviderClaudeCLI,
			APIKey:       "sk-secret",
			Enabled:      enabled,
		}
	}
	rows := map[string]*store.LLMProviderData{
		"master-shared":     mk(store.MasterTenantID, "shared", true),
		"master-overridden": mk(store.MasterTenantID, "openrouter", true),
		"master-disabled":   mk(store.MasterTenantID, "old", false),
		"tenant-own":        mk(tenantID, "openrouter", true),
		// A disabled tenant provider is not registered at runtime, so the master one of that name is used.
		"tenant-disabled": mk(tenantID, "deepseek", false),
		"master-deepseek": mk(store.MasterTenantID, "deepseek", true),
	}
	s := &tenantProviderStore{mockProviderStore: newMockProviderStore()}
	for _, p := range rows {
		s.rows = append(s.rows, p)
	}
	return s, tenantID, rows
}

func listProvidersAs(t *testing.T, h *ProvidersHandler, tenantID uuid.UUID) []providerResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/providers", nil)
	req = req.WithContext(store.WithTenantID(req.Context(), tenantID))
	w := httptest.NewRecorder()
	h.handleListProviders(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Providers []providerResponse `json:"providers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	return body.Providers
}

func TestListProvidersIncludesInheritedMasterProviders(t *testing.T) {
	s, tenantID, rows := newInheritedFixture()
	h := NewProvidersHandler(s, newMockSecretsStore(), nil, "")

	got := listProvidersAs(t, h, tenantID)
	byID := make(map[uuid.UUID]providerResponse, len(got))
	for _, p := range got {
		byID[p.ID] = p
	}
	if len(got) != 4 {
		t.Fatalf("got %d providers, want 4 (2 own + 2 inherited): %+v", len(got), got)
	}
	if p, ok := byID[rows["tenant-disabled"].ID]; !ok || p.Inherited {
		t.Fatalf("disabled own provider missing or marked inherited: %+v", p)
	}
	if p, ok := byID[rows["master-deepseek"].ID]; !ok || !p.Inherited {
		t.Fatalf("master provider shadowed only by a disabled own one must be inherited: %+v", p)
	}
	if p, ok := byID[rows["tenant-own"].ID]; !ok || p.Inherited {
		t.Fatalf("tenant-own provider missing or marked inherited: %+v", p)
	}
	shared, ok := byID[rows["master-shared"].ID]
	if !ok || !shared.Inherited {
		t.Fatalf("master-shared should be listed as inherited: %+v", shared)
	}
	if shared.APIKey != "***" {
		t.Fatalf("inherited api_key = %q, want masked", shared.APIKey)
	}
	if _, ok := byID[rows["master-overridden"].ID]; ok {
		t.Fatal("master provider overridden by tenant name must be hidden")
	}
	if _, ok := byID[rows["master-disabled"].ID]; ok {
		t.Fatal("disabled master provider must be hidden")
	}
}

func TestListProvidersMasterTenantHasNoInherited(t *testing.T) {
	s, _, _ := newInheritedFixture()
	h := NewProvidersHandler(s, newMockSecretsStore(), nil, "")

	for _, p := range listProvidersAs(t, h, store.MasterTenantID) {
		if p.Inherited {
			t.Fatalf("master tenant must not see inherited providers: %+v", p)
		}
	}
}

func TestGetProviderForRead(t *testing.T) {
	s, tenantID, rows := newInheritedFixture()
	h := NewProvidersHandler(s, newMockSecretsStore(), nil, "")
	ctx := store.WithTenantID(context.Background(), tenantID)

	tests := []struct {
		name          string
		id            uuid.UUID
		wantFound     bool
		wantInherited bool
	}{
		{"own provider", rows["tenant-own"].ID, true, false},
		{"inherited master provider", rows["master-shared"].ID, true, true},
		{"overridden master provider", rows["master-overridden"].ID, false, false},
		{"disabled master provider", rows["master-disabled"].ID, false, false},
		{"master provider behind a disabled own one", rows["master-deepseek"].ID, true, true},
		{"unknown id", uuid.New(), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, inherited, err := h.getProviderForRead(ctx, tt.id)
			if (err == nil) != tt.wantFound {
				t.Fatalf("err = %v, wantFound %v", err, tt.wantFound)
			}
			if tt.wantFound && (p == nil || p.ID != tt.id) {
				t.Fatalf("provider = %+v, want id %s", p, tt.id)
			}
			if inherited != tt.wantInherited {
				t.Fatalf("inherited = %v, want %v", inherited, tt.wantInherited)
			}
		})
	}
}

func TestInheritedProvidersAreReadOnly(t *testing.T) {
	s, tenantID, rows := newInheritedFixture()
	h := NewProvidersHandler(s, newMockSecretsStore(), nil, "")
	id := rows["master-shared"].ID.String()

	for _, tc := range []struct {
		method string
		body   string
		handle func(http.ResponseWriter, *http.Request)
	}{
		{http.MethodDelete, "", h.handleDeleteProvider},
		{http.MethodPut, `{"api_base":"https://evil.example"}`, h.handleUpdateProvider},
	} {
		req := httptest.NewRequest(tc.method, "/v1/providers/"+id, strings.NewReader(tc.body))
		req.SetPathValue("id", id)
		req = req.WithContext(store.WithTenantID(req.Context(), tenantID))
		w := httptest.NewRecorder()
		tc.handle(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s inherited provider: status = %d, want 404 (body=%s)", tc.method, w.Code, w.Body.String())
		}
	}
	if rows["master-shared"].APIBase != "" {
		t.Fatalf("master provider changed through a tenant request: %+v", rows["master-shared"])
	}
}
