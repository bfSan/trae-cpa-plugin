package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestModelOrderConfigAndRuntime(t *testing.T) {
	prior := currentModelOrder()
	defer func() { modelOrderState.Lock(); modelOrderState.ids = prior; modelOrderState.Unlock() }()
	if err := configureModelOrder([]byte("model_order: [b, a, b]\n")); err != nil {
		t.Fatal(err)
	}
	models := []pluginapi.ModelInfo{{ID: "a-solo", ContextLength: 64000}, {ID: "b-solo", ContextLength: 128000}, {ID: "c-solo"}}
	got := applyModelOrder(models)
	if got[0].ID != "b-solo" || got[1].ID != "a-solo" || got[0].ContextLength != 128000 {
		t.Fatalf("bad runtime order: %+v", got)
	}
	if models[0].ID != "a-solo" {
		t.Fatal("mutated source catalog")
	}
	for _, invalid := range []string{"model_order: string", "model_order: [12]", "model_order: ['']"} {
		if configureModelOrder([]byte(invalid)) == nil {
			t.Fatal("accepted invalid order")
		}
	}
	if currentModelOrder()[0] != "b" {
		t.Fatal("invalid config destroyed last-good order")
	}
	if err := configureModelOrder([]byte("login_variant: cn")); err != nil {
		t.Fatal(err)
	}
	if currentModelOrder()[0] != "b" {
		t.Fatal("partial configure reset order")
	}
	if err := configureModelOrder([]byte("model_order: []")); err != nil {
		t.Fatal(err)
	}
	if len(currentModelOrder()) != 0 {
		t.Fatal("explicit reset failed")
	}
}

func TestModelGroupsUnionRetainsAccountDifferences(t *testing.T) {
	entries := []*variantCatalogEntry{
		{name: "old", snapshot: &persistedModelCache{Realm: "cn", FetchedAt: "2026-01-01T00:00:00Z", Models: []pluginapi.ModelInfo{{ID: "a", Name: "A", ContextLength: 64000}, {ID: "only-old"}}}},
		{name: "new", snapshot: &persistedModelCache{Realm: "cn", FetchedAt: "2026-02-01T00:00:00Z", Models: []pluginapi.ModelInfo{{ID: "a", Name: "A", ContextLength: 128000}, {ID: "only-new"}}}},
	}
	g := buildModelGroup("cn", entries, false)
	if g.Count != 3 || g.Credentials != 2 {
		t.Fatalf("lost account contribution: %+v", g)
	}
	for _, m := range g.Models {
		if m.ID == "a" {
			if m.Credentials != 2 || m.ContextLength != 0 || len(m.ContextLengths) != 2 || m.Multiplier != nil || m.Thinking != nil {
				t.Fatalf("fabricated capability or lost differences: %+v", m)
			}
		}
	}
}

func TestModelGroupsRefreshQueriesEveryCredential(t *testing.T) {
	restore := stubPersistSeams(t, map[string][]byte{})
	defer restore()
	calls := map[string]bool{}
	restoreFetch := stubCNDiscovery(t, func(a *auth.Auth) ([]upstream.ModelInfo, error) {
		calls[a.AccessToken] = true
		return []upstream.ModelInfo{{ID: a.AccessToken}}, nil
	})
	defer restoreFetch()
	g := buildModelGroup("cn", []*variantCatalogEntry{{name: "one", variant: "cn", hasToken: true, storage: persistTestStorage("one", "cn", "")}, {name: "two", variant: "cn", hasToken: true, storage: persistTestStorage("two", "cn", "")}}, true)
	if len(calls) != 2 || g.Count != 2 {
		t.Fatalf("refresh only representative: calls=%v group=%+v", calls, g)
	}
}

func TestUnknownAuthNeverUsesGlobalOrPeerCatalog(t *testing.T) {
	restore := stubPersistSeams(t, map[string][]byte{"peer": mustPersistCache(t, persistTestStorage("peer", "cn", ""), "cn", "2026-01-01T00:00:00Z", variantTestModels("peer-premium"))})
	defer restore()
	restoreFetch := stubCNDiscovery(t, func(*auth.Auth) ([]upstream.ModelInfo, error) { return nil, errors.New("offline") })
	defer restoreFetch()
	for _, handle := range []func([]byte) ([]byte, error){handleModelForAuth, intlhandleModelForAuth} {
		request, _ := json.Marshal(map[string]any{"StorageJSON": []byte(`invalid`)})
		out, err := handle(request)
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Data pluginapi.ModelResponse `json:"result"`
		}
		if json.Unmarshal(out, &env) != nil {
			t.Fatal("bad envelope")
		}
		if len(env.Data.Models) != 0 {
			t.Fatalf("unknown account got models: %s", out)
		}
	}
	out, err := handleModelStatic([]byte(`{}`))
	if err != nil || strings.Contains(string(out), "peer-premium") {
		t.Fatalf("static must not grant peers: %s %v", out, err)
	}
}

func TestModelExclusionWildcardRetainsSuffixIsolation(t *testing.T) {
	host := pluginapi.HostConfigSummary{ExcludedModels: map[string][]string{"trae-solo": {"G*SOLO"}}}
	got := filterExcludedModels([]pluginapi.ModelInfo{{ID: "gpt-solo"}, {ID: "gpt-intl"}, {ID: "gpt"}}, host, "trae-solo")
	assertTraeModelIDs(t, got, []string{"gpt-intl", "gpt"})
}

func TestAccountPatchPreservesUnknownAndDisabledReason(t *testing.T) {
	raw := []byte(`{"auth":{"accessToken":"token"},"unknown":{"nested":[1,2]},"disabled":true,"disabled_reason":"manual","model_cache":{"models":["a"]}}`)
	for _, action := range []string{"rename", "disable"} {
		result, err := patchAccountDocument(raw, action, "New label", "")
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]json.RawMessage
		_ = json.Unmarshal(result, &doc)
		if string(doc["unknown"]) != `{"nested":[1,2]}` || string(doc["disabled_reason"]) != `"manual"` || string(doc["disabled"]) != "true" || string(doc["auth"]) != `{"accessToken":"token"}` {
			t.Fatalf("lost operator/token fields: %s", result)
		}
	}
	result, err := patchAccountDocument(raw, "enable", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(result, &doc)
	if _, ok := doc["disabled_reason"]; ok {
		t.Fatal("enable retained stale disabled reason")
	}
	if string(doc["disabled"]) != "false" {
		t.Fatal("enable failed")
	}
}

func TestModelGroupsDisabledCredentialNotRefreshed(t *testing.T) {
	restore := stubCNDiscovery(t, func(*auth.Auth) ([]upstream.ModelInfo, error) {
		t.Fatal("disabled credential was refreshed")
		return nil, nil
	})
	defer restore()
	g := buildModelGroup("cn", []*variantCatalogEntry{{variant: "cn", hasToken: true, disabled: true, storage: persistTestStorage("disabled", "cn", ""), snapshot: &persistedModelCache{Realm: "cn", Models: variantTestModels("known")}}}, true)
	if g.Count != 1 || g.Models[0].ID != "known" {
		t.Fatal("disabled account snapshot lost from candidate catalog")
	}
}

func TestStaticCatalogCannotGrantGuessedModels(t *testing.T) {
	for _, handle := range []func([]byte) ([]byte, error){handleModelStatic, intlhandleModelStatic} {
		raw, err := handle([]byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Result pluginapi.ModelResponse `json:"result"`
		}
		if err = json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.Result.Provider == "" || env.Result.Models == nil || len(env.Result.Models) != 0 {
			t.Fatalf("static response can grant models: %s", raw)
		}
	}
}

func TestSnapshotLookupNeverCrossesVariantWithSameToken(t *testing.T) {
	restore := stubPersistSeams(t, map[string][]byte{"solo": mustPersistCache(t, persistTestStorage("same-token", "solo", ""), "solo", "2026-01-01T00:00:00Z", variantTestModels("premium-solo"))})
	defer restore()
	if _, _, ok := locateCredentialFile(persistTestStorage("same-token", "cn", "")); ok {
		t.Fatal("cross-variant credential matched")
	}
}
