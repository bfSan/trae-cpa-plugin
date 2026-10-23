package main

import (
	"testing"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
)

func boolPtr(v bool) *bool { return &v }

func TestThinkingSupportForFactsUsesAuxiliaryCatalogWithHostVocabulary(t *testing.T) {
	f := upstream.ModelFacts{CapabilityCatalog: []upstream.ModelCapability{
		{SourceFunction: "solo_agent", ThinkingOptions: []string{"light", "high", "extra_high"}, ThinkingDefault: "high"},
	}}
	got := thinkingSupportForFacts(f, "cn")
	if got == nil || len(got.Levels) != 3 || got.Levels[0] != "high" || got.Levels[1] != "low" || got.Levels[2] != "xhigh" {
		t.Fatalf("unexpected host levels: %+v", got)
	}
}

func TestThinkingSupportForFactsUnknownAndUnsupportedStayNil(t *testing.T) {
	for name, f := range map[string]upstream.ModelFacts{
		"empty":       {},
		"unsupported": {CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_work_lite", ThinkingSupported: boolPtr(false), ThinkingOptions: []string{"high"}}}},
		"no levels":   {CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_work_lite", ThinkingDefault: "high"}}},
	} {
		if got := thinkingSupportForFacts(f, "cn"); got != nil {
			t.Fatalf("%s: invented support %+v", name, got)
		}
	}
}

func TestThinkingSupportForFactsUnmappedVocabularyStaysNil(t *testing.T) {
	f := upstream.ModelFacts{CapabilityCatalog: []upstream.ModelCapability{
		{SourceFunction: "solo_agent", ThinkingOptions: []string{"turbo"}},
	}}
	if got := thinkingSupportForFacts(f, "cn"); got != nil {
		t.Fatalf("unknown vocabulary must not be passed through: %+v", got)
	}
}

func TestThinkingSupportForFactsDoesNotAliasCatalog(t *testing.T) {
	f := upstream.ModelFacts{CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_work_lite", ThinkingOptions: []string{"low", "high"}}}}
	got := thinkingSupportForFacts(f, "cn")
	if got == nil {
		t.Fatal("expected levels")
	}
	got.Levels[0] = "mutated"
	if f.CapabilityCatalog[0].ThinkingOptions[0] != "low" {
		t.Fatal("levels alias the catalog slice")
	}
}

func TestThinkingSupportForFactsPrefersServingLane(t *testing.T) {
	f := upstream.ModelFacts{CapabilityCatalog: []upstream.ModelCapability{
		{SourceFunction: "solo_work_lite", ThinkingOptions: []string{"high"}},
		{SourceFunction: "solo_agent", ThinkingOptions: []string{"light", "high", "extra_high"}},
	}}
	got := thinkingSupportForFacts(f, "cn")
	if got == nil || len(got.Levels) != 1 || got.Levels[0] != "high" {
		t.Fatalf("serving lane must win when it declares tiers: %+v", got)
	}
}

func TestThinkingSupportForFactsFallsBackToAuxiliary(t *testing.T) {
	f := upstream.ModelFacts{CapabilityCatalog: []upstream.ModelCapability{
		{SourceFunction: "solo_agent", ThinkingOptions: []string{"light", "extra_high"}},
	}}
	got := thinkingSupportForFacts(f, "cn")
	if got == nil || len(got.Levels) != 2 || got.Levels[0] != "low" || got.Levels[1] != "xhigh" {
		t.Fatalf("auxiliary tiers must be reported when serving lane is silent: %+v", got)
	}
}
