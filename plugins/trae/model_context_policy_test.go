package main

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestContextPolicySchemaMigrationRespectsRealmAndRetryBudget(t *testing.T) {
	now := time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, realm, status string
		age                 time.Duration
		want                bool
	}{
		{"cn updated migrates immediately", "cn", "updated", time.Hour, true},
		{"solo updated migrates immediately", "solo", "updated", time.Hour, true},
		{"intl migrates once for grouped catalog decoder", "intl", "updated", time.Hour, true},
		{"failed attempt throttles migration", "cn", "failed", time.Hour, false},
		{"partial attempt throttles migration", "cn", "partial", time.Hour, false},
		{"checkpoint throttles migration after restart", "cn", "in_progress", time.Hour, false},
		{"failed attempt retries next day", "cn", "failed", 24 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := now.Add(-tc.age).Format(time.RFC3339Nano)
			raw, _ := mergeModelCacheIntoDoc([]byte(`{}`), &persistedModelCache{SchemaVersion: 2, Realm: tc.realm, FetchedAt: at, Models: variantTestModels("legacy")})
			raw, _ = mergeModelRefreshState(raw, modelRefreshState{SchemaVersion: 2, Realm: tc.realm, LastAttemptAt: at, Status: tc.status})
			if got := modelRefreshDue(raw, tc.realm, now); got != tc.want {
				t.Fatalf("due=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestContextPolicyLegacyCachePreservedOnFailedMigration(t *testing.T) {
	f, calls := mockModelRefresh(t)
	legacy := &persistedModelCache{SchemaVersion: 2, Realm: "cn", FetchedAt: modelRefreshNow().Add(-time.Hour).Format(time.RFC3339Nano), Models: []pluginapi.ModelInfo{{ID: "m", ContextLength: 200000}}, Facts: map[string]upstream.ModelFacts{"m": {CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_agent", Contexts: map[string]int64{"max": 1000000}}}}}}
	f.doc, _ = mergeModelCacheIntoDoc(f.doc, legacy)
	f.doc, _ = mergeModelRefreshState(f.doc, modelRefreshState{SchemaVersion: 2, Realm: "cn", LastAttemptAt: legacy.FetchedAt, Status: "updated"})
	fetchModelsFn = func(*auth.Auth) ([]upstream.ModelInfo, error) {
		calls.Add(1)
		return nil, errors.New("fixture catalog unavailable")
	}
	result := refreshAccountModels("idx", false)
	if result.Updated || result.Status != "failed" || len(result.Models) != 1 || result.Models[0].ContextLength != 200000 || calls.Load() != 1 {
		t.Fatalf("migration failure lost last-good: %+v", result)
	}
	got := readModelCacheDoc(f.doc)
	before, _ := json.Marshal(legacy)
	after, _ := json.Marshal(got)
	if string(before) != string(after) {
		t.Fatal("migration failure recomputed or modified legacy cache")
	}
	if st := readModelRefreshState(f.doc); st.SchemaVersion != modelCacheSchemaVersion || st.Status != "failed" {
		t.Fatalf("missing new-policy checkpoint: %+v", st)
	}
	refreshAccountModels("idx", false)
	if calls.Load() != 1 {
		t.Fatal("failed migration retried before daily interval")
	}
}

func TestContextPolicyMigrationRegistersEffectiveWindowWithoutReceipt(t *testing.T) {
	f, calls := mockModelRefresh(t)
	f.doc, _ = mergeModelCacheIntoDoc(f.doc, &persistedModelCache{SchemaVersion: 2, Realm: "cn", FetchedAt: modelRefreshNow().Format(time.RFC3339Nano), Models: []pluginapi.ModelInfo{{ID: "m", ContextLength: 200000}}})
	fetchModelsFn = func(*auth.Auth) ([]upstream.ModelInfo, error) {
		calls.Add(1)
		return []upstream.ModelInfo{{ID: "m", ContextWindow: 981504, MaxTokens: 4096, Facts: upstream.ModelFacts{Context: 200000, Contexts: map[string]int64{"dev": 200000}, CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_agent", Contexts: map[string]int64{"max": 981504}}}}}}, nil
	}
	a, _ := parseStoredAuth(f.doc)
	models := accountModelsForStorage(a, f.doc, false)
	if len(models) != 1 || models[0].ContextLength != 981504 || models[0].MaxCompletionTokens != 4096 || calls.Load() != 1 {
		t.Fatalf("registration=%+v calls=%d", models, calls.Load())
	}
	cache := readModelCacheDoc(f.doc)
	if cache.SchemaVersion != modelCacheSchemaVersion || cache.Facts["m"].ContextEvidence != nil || cache.Facts["m"].Context != 200000 {
		t.Fatalf("registration invented evidence or lost raw primary declaration: %+v", cache)
	}
}

func TestContextReceiptNeverOverridesDeclaredEffectiveWindow(t *testing.T) {
	a := &auth.Auth{UID: "fixture-a", Variant: "cn"}
	m := upstream.ModelInfo{ID: "deepseek-v4.1-flash", ContextWindow: 1500000, Facts: upstream.ModelFacts{CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_agent", ContextOptions: map[string][]int64{"max": {1000000, 1500000}}}}}}
	e := storedContextEvidence{ContextEvidence: upstream.ContextEvidence{Kind: "upstream_usage_and_markers", Realm: "cn", Function: "solo_work_lite", Model: m.ID, ObservedInputTokens: 990767, DeclaredContext: 1000000, ObservedAt: time.Now().UTC().Format(time.RFC3339), EvidenceID: "fixture-receipt"}, AccountScopeHash: modelEvidenceScope(a), MarkerCheckPassed: true}
	raw, _ := json.Marshal(map[string]any{"model_context_evidence": map[string]storedContextEvidence{m.ID: e}})
	out := applyModelContextEvidence(a, raw, []upstream.ModelInfo{m})
	if out[0].ContextWindow != 1500000 || out[0].Facts.ContextEvidence == nil || out[0].Facts.ContextEvidence.DeclaredContext != 1000000 {
		t.Fatalf("receipt changed declared context or overstated tested scope: %+v", out)
	}
	m.Facts.ContextEvidence = &e.ContextEvidence
	for _, credential := range []*auth.Auth{nil, {Variant: "cn"}, {UID: "fixture-a", Variant: "intl"}} {
		got := applyModelContextEvidence(credential, []byte(`{}`), []upstream.ModelInfo{m})
		if got[0].Facts.ContextEvidence != nil {
			t.Fatal("inherited evidence without valid credential-local receipt")
		}
	}
}
