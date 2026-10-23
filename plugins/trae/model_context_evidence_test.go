package main

import (
	"encoding/json"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"testing"
	"time"
)

func TestModelContextEvidenceIsAccountAndLaneScoped(t *testing.T) {
	a := &auth.Auth{UID: "fixture-a", Variant: "cn"}
	yes := true
	base := []upstream.ModelInfo{{ID: "deepseek-v4.1-flash", ContextWindow: 1000000, Facts: upstream.ModelFacts{Contexts: map[string]int64{"dev": 200000}, CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_agent", Contexts: map[string]int64{"dev": 200000, "max": 1000000}, MaxMode: &yes}}}}}
	valid := storedContextEvidence{ContextEvidence: upstream.ContextEvidence{Kind: "upstream_usage_and_markers", Realm: "cn", Function: "solo_work_lite", Model: "deepseek-v4.1-flash", ObservedInputTokens: 990767, DeclaredContext: 1000000, ObservedAt: time.Now().UTC().Format(time.RFC3339), EvidenceID: "fixture-receipt"}, AccountScopeHash: modelEvidenceScope(a), MarkerCheckPassed: true}
	for _, tc := range []struct {
		name   string
		mutate func(*storedContextEvidence)
		pass   bool
	}{
		{"verified receipt", func(e *storedContextEvidence) {}, true},
		{"other account", func(e *storedContextEvidence) { e.AccountScopeHash = "another" }, false},
		{"other realm", func(e *storedContextEvidence) { e.Realm = "intl" }, false},
		{"other function", func(e *storedContextEvidence) { e.Function = "solo_agent" }, false},
		{"no position check", func(e *storedContextEvidence) { e.MarkerCheckPassed = false }, false},
		{"only short test", func(e *storedContextEvidence) { e.ObservedInputTokens = 128 }, false},
		{"no upper declaration", func(e *storedContextEvidence) { e.DeclaredContext = 2000000 }, false},
		{"historical evidence still requires current declaration", func(e *storedContextEvidence) {
			e.ObservedAt = time.Now().Add(-91 * 24 * time.Hour).UTC().Format(time.RFC3339)
		}, true},
		{"future evidence", func(e *storedContextEvidence) { e.ObservedAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := valid
			tc.mutate(&e)
			raw, _ := json.Marshal(map[string]any{"model_context_evidence": map[string]storedContextEvidence{e.Model: e}})
			out := applyModelContextEvidence(a, raw, base)
			want := int64(1000000)
			if (out[0].Facts.ContextEvidence != nil) != tc.pass {
				t.Fatalf("receipt validation: %+v", out[0].Facts.ContextEvidence)
			}
			if out[0].ContextWindow != want {
				t.Fatalf("context=%d want %d", out[0].ContextWindow, want)
			}
			if base[0].ContextWindow != 1000000 {
				t.Fatal("mutated base catalog")
			}
			if out[0].Facts.Contexts["dev"] != 200000 {
				t.Fatal("rewrote upstream dev evidence")
			}
		})
	}
	raw, _ := json.Marshal(map[string]any{"model_context_evidence": map[string]storedContextEvidence{valid.Model: valid}})
	own := applyModelContextEvidence(&auth.Auth{UID: "fixture-b", Variant: "cn"}, raw, base)
	if own[0].ContextWindow != 1000000 || own[0].Facts.ContextEvidence != nil {
		t.Fatal("copied evidence expanded another account")
	}
	missing := append([]upstream.ModelInfo(nil), base...)
	missing[0].Facts.CapabilityCatalog = nil
	missing[0].ContextWindow = 200000
	if got := applyModelContextEvidence(a, raw, missing); got[0].ContextWindow != 200000 {
		t.Fatal("missing latest declaration should not expand")
	}
}
