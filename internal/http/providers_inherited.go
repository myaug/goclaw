package http

import (
	"context"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// providerResponse is the wire shape for provider reads. Inherited marks an
// enabled master-tenant provider that a non-master tenant uses at runtime via
// the name fallback in providers.Registry.GetForTenant. Inherited providers are
// read-only for the tenant: mutations stay tenant-scoped and return 404.
type providerResponse struct {
	store.LLMProviderData
	Inherited bool `json:"inherited,omitempty"`
}

func newProviderResponse(p *store.LLMProviderData, inherited bool) providerResponse {
	maskAPIKey(p)
	return providerResponse{LLMProviderData: canonicalizeProviderForResponse(p), Inherited: inherited}
}

// inheritsMasterProviders reports whether the tenant in ctx falls back to
// master-tenant providers at runtime (see providers.Registry.GetForTenant).
func inheritsMasterProviders(ctx context.Context) bool {
	if store.IsCrossTenant(ctx) {
		return false
	}
	tid := store.TenantIDFromContext(ctx)
	return tid != uuid.Nil && tid != store.MasterTenantID
}

// listInheritedProviders returns enabled master-tenant providers whose name is
// not overridden by one of the tenant's own enabled providers. Disabled providers
// are never registered in the runtime registry (registerProvidersFromDB), so a
// disabled master provider is not usable and a disabled tenant provider does not
// override: GetForTenant then falls back to the master provider of that name.
func (h *ProvidersHandler) listInheritedProviders(ctx context.Context, own []store.LLMProviderData) ([]store.LLMProviderData, error) {
	if !inheritsMasterProviders(ctx) {
		return nil, nil
	}
	master, err := h.store.ListProviders(store.WithTenantID(ctx, store.MasterTenantID))
	if err != nil {
		return nil, err
	}
	ownNames := make(map[string]struct{}, len(own))
	for i := range own {
		if own[i].Enabled {
			ownNames[own[i].Name] = struct{}{}
		}
	}
	var inherited []store.LLMProviderData
	for _, p := range master {
		if !p.Enabled {
			continue
		}
		if _, overridden := ownNames[p.Name]; overridden {
			continue
		}
		inherited = append(inherited, p)
	}
	return inherited, nil
}

// getProviderForRead resolves a provider by ID for read-only endpoints. It
// returns the tenant's own provider, or an inherited master provider when the
// tenant has no enabled provider of that name (same rule as listInheritedProviders).
// The returned bool reports inheritance.
func (h *ProvidersHandler) getProviderForRead(ctx context.Context, id uuid.UUID) (*store.LLMProviderData, bool, error) {
	p, err := h.store.GetProvider(ctx, id)
	if err == nil || !inheritsMasterProviders(ctx) {
		return p, false, err
	}
	mp, merr := h.store.GetProvider(store.WithTenantID(ctx, store.MasterTenantID), id)
	if merr != nil || !mp.Enabled {
		return nil, false, err
	}
	if own, oerr := h.store.GetProviderByName(ctx, mp.Name); oerr == nil && own != nil && own.Enabled {
		return nil, false, err
	}
	return mp, true, nil
}
