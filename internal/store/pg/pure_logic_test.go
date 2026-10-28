package pg

import (
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// --- config_permissions.go: evalPermRows + matchWildcard + itoa ---

func TestMatchWildcard(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"group:*", "group:telegram:123", true},
		{"group:*", "group:telegram", true},
		{"group:*", "dm:telegram", false},
		{"group:telegram:*", "group:telegram:123", true},
		{"group:telegram:*", "group:telegram", true},
		{"group:telegram:*", "group:slack:123", false},
		{"exact", "exact", true},
		{"exact", "other", false},
		{"group:telegram:-100456", "group:telegram:-100456", true},
		{"group:telegram:-100456", "group:telegram:-100457", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+" vs "+tc.value, func(t *testing.T) {
			if got := matchWildcard(tc.pattern, tc.value); got != tc.want {
				t.Errorf("matchWildcard(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
			}
		})
	}
}

func TestEvalPermRows_IndividualDenyWinsOverGroupAllow(t *testing.T) {
	rows := []permRow{
		{Scope: "*", ConfigType: "*", UserID: "*", Permission: "allow"},
		{Scope: "*", ConfigType: "*", UserID: "alice", Permission: "deny"},
	}
	if got := evalPermRows(rows, "any", "any", "alice"); got {
		t.Error("individual deny should win over group allow")
	}
}

func TestEvalPermRows_IndividualAllowWinsOverGroupDeny(t *testing.T) {
	rows := []permRow{
		{Scope: "*", ConfigType: "*", UserID: "*", Permission: "deny"},
		{Scope: "*", ConfigType: "*", UserID: "alice", Permission: "allow"},
	}
	if got := evalPermRows(rows, "any", "any", "alice"); !got {
		t.Error("individual allow should win over group deny")
	}
}

func TestEvalPermRows_GroupAllowWhenNoIndividual(t *testing.T) {
	rows := []permRow{
		{Scope: "*", ConfigType: "*", UserID: "*", Permission: "allow"},
	}
	if got := evalPermRows(rows, "any", "any", "bob"); !got {
		t.Error("group allow should apply when no individual rule")
	}
}

func TestEvalPermRows_GroupDenyWhenNoIndividual(t *testing.T) {
	rows := []permRow{
		{Scope: "*", ConfigType: "*", UserID: "*", Permission: "deny"},
	}
	if got := evalPermRows(rows, "any", "any", "bob"); got {
		t.Error("group deny should apply when no individual rule")
	}
}

func TestEvalPermRows_NoMatchDefaultsDeny(t *testing.T) {
	rows := []permRow{
		{Scope: "different", ConfigType: "*", UserID: "alice", Permission: "allow"},
	}
	// scope doesn't match → no rule applies → implicit deny.
	if got := evalPermRows(rows, "other", "any", "alice"); got {
		t.Error("no matching rule should default to deny")
	}
}

func TestEvalPermRows_ScopeFiltering(t *testing.T) {
	rows := []permRow{
		{Scope: "group:*", ConfigType: "*", UserID: "*", Permission: "allow"},
	}
	if !evalPermRows(rows, "group:telegram:1", "skill", "u") {
		t.Error("wildcarded scope should match")
	}
	if evalPermRows(rows, "dm:telegram", "skill", "u") {
		t.Error("wildcarded scope should NOT match different prefix")
	}
}

func TestEvalPermRows_ConfigTypeFiltering(t *testing.T) {
	rows := []permRow{
		{Scope: "*", ConfigType: "skill", UserID: "u", Permission: "allow"},
	}
	if !evalPermRows(rows, "any", "skill", "u") {
		t.Error("exact config_type match should allow")
	}
	if evalPermRows(rows, "any", "other", "u") {
		t.Error("different config_type should not allow")
	}
}

func TestItoa(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{-5, "-5"},
		{1000, "1000"},
	}
	for _, tc := range cases {
		if got := itoa(tc.n); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// --- contact_resolve.go: contactResolveCache ---

func TestContactResolveCache_GetSet(t *testing.T) {
	c := newContactResolveCache()

	// Miss.
	if v, ok := c.get("k1"); ok {
		t.Errorf("expected miss, got %q", v)
	}

	// Set and hit.
	c.set("k1", "tenant-user-1")
	if v, ok := c.get("k1"); !ok || v != "tenant-user-1" {
		t.Errorf("get after set: got %q ok=%v", v, ok)
	}

	// Set another key without interfering.
	c.set("k2", "tenant-user-2")
	if v, ok := c.get("k2"); !ok || v != "tenant-user-2" {
		t.Errorf("k2: got %q ok=%v", v, ok)
	}
	if v, ok := c.get("k1"); !ok || v != "tenant-user-1" {
		t.Errorf("k1 after k2 set: got %q ok=%v", v, ok)
	}
}

func TestContactResolveCache_NegativeResult(t *testing.T) {
	c := newContactResolveCache()
	// Negative results are cached as empty string with ok=true.
	c.set("k1", "")
	v, ok := c.get("k1")
	if !ok {
		t.Error("negative cache entry should be present")
	}
	if v != "" {
		t.Errorf("negative v = %q, want empty", v)
	}
}

func TestContactResolveCache_ExpiredEntry(t *testing.T) {
	c := newContactResolveCache()
	// Manually insert a stale entry.
	c.items["k1"] = contactResolveEntry{
		tenantUserID: "old-user",
		fetched:      time.Now().Add(-2 * contactResolveCacheTTL),
	}
	if _, ok := c.get("k1"); ok {
		t.Error("expired entry should not be returned as hit")
	}
}

// --- usage_pricing.go: usagePricingModelCandidates + DeepSeek native aliases ---

func TestUsagePricingModelCandidatesDeepSeekNativeAliases(t *testing.T) {
	cases := []struct {
		name         string
		providerName string
		providerType string
		modelID      string
		want         []pricingCandidate
	}{
		{
			name:         "deepseek flash native expands to versioned snapshot prefixes",
			providerName: "deepseek", providerType: store.ProviderDeepSeek, modelID: "deepseek-flash",
			want: []pricingCandidate{
				{value: "deepseek-flash"},
				{value: "deepseek/deepseek-flash"},
				{value: "deepseek/deepseek-v4.1-flash", prefix: true},
				{value: "deepseek/deepseek-v4-flash", prefix: true},
			},
		},
		{
			name:         "deepseek v4 pro native expands to versioned snapshot prefix",
			providerName: "deepseek", providerType: store.ProviderDeepSeek, modelID: "deepseek-v4-pro",
			want: []pricingCandidate{
				{value: "deepseek-v4-pro"},
				{value: "deepseek/deepseek-v4-pro"},
				{value: "deepseek/deepseek-v4-pro", prefix: true},
			},
		},
		{
			name:         "deepseek chat native has no snapshot alias",
			providerName: "deepseek", providerType: store.ProviderDeepSeek, modelID: "deepseek-chat",
			want: []pricingCandidate{
				{value: "deepseek-chat"},
				{value: "deepseek/deepseek-chat"},
			},
		},
		{
			name:         "non-deepseek provider unaffected",
			providerName: "gemini", providerType: store.ProviderGeminiNative, modelID: "gemini-2.5-flash",
			want: []pricingCandidate{
				{value: "gemini-2.5-flash"},
				{value: "google/gemini-2.5-flash"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := usagePricingModelCandidates(tc.providerName, tc.providerType, tc.modelID)
			if len(got) != len(tc.want) {
				t.Fatalf("candidates = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("candidate[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestEscapeLikePrefix(t *testing.T) {
	cases := map[string]string{
		"deepseek/deepseek-v4.1-flash": `deepseek/deepseek-v4.1-flash`,
		"a%b_c":                        `a\%b\_c`,
		`back\slash`:                   `back\\slash`,
	}
	for in, want := range cases {
		if got := escapeLikePrefix(in); got != want {
			t.Fatalf("escapeLikePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
