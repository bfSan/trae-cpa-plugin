package main

import (
	"testing"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
)

func boolPtr(v bool) *bool { return &v }

func TestThinkingSupportForFactsServingLaneOnly(t *testing.T) {
	f := upstream.ModelFacts{CapabilityCatalog: []upstream.ModelCapability{
		{SourceFunction: "solo_agent", ThinkingOptions: []string{"light", "high", "extra_high"}, ThinkingDefault: "high"},
	}}
	if got := thinkingSupportForFacts(f, "solo_work_lite"); got != nil {
		t.Fatalf("auxiliary-only declaration must not be advertised on the serving lane: %+v", got)
	}
	got := thinkingSupportForFacts(f, "solo_agent")
	if got == nil || len(got.Levels) != 3 || got.Levels[0] != "extra_high" || got.Levels[1] != "high" || got.Levels[2] != "light" {
		t.Fatalf("unexpected levels: %+v", got)
	}
}

func TestThinkingSupportForFactsUnknownAndUnsupportedStayNil(t *testing.T) {
	for name, f := range map[string]upstream.ModelFacts{
		"empty":       {},
		"unsupported": {CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_work_lite", ThinkingSupported: boolPtr(false), ThinkingOptions: []string{"high"}}}},
		"no levels":   {CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_work_lite", ThinkingDefault: "high"}}},
	} {
		if got := thinkingSupportForFacts(f, "solo_work_lite"); got != nil {
			t.Fatalf("%s: invented support %+v", name, got)
		}
	}
}

func TestThinkingSupportForFactsContradictorySourcesStayNil(t *testing.T) {
	f := upstream.ModelFacts{CapabilityCatalog: []upstream.ModelCapability{
		{SourceFunction: "solo_work_lite", ThinkingOptions: []string{"low", "high"}},
		{SourceFunction: "solo_work_lite", ThinkingOptions: []string{"high", "max"}},
	}}
	if got := thinkingSupportForFacts(f, "solo_work_lite"); got != nil {
		t.Fatalf("contradictory declarations must not be merged: %+v", got)
	}
}

func TestThinkingSupportForFactsDoesNotAliasCatalog(t *testing.T) {
	f := upstream.ModelFacts{CapabilityCatalog: []upstream.ModelCapability{{SourceFunction: "solo_work_lite", ThinkingOptions: []string{"low", "high"}}}}
	got := thinkingSupportForFacts(f, "solo_work_lite")
	if got == nil {
		t.Fatal("expected levels")
	}
	got.Levels[0] = "mutated"
	if f.CapabilityCatalog[0].ThinkingOptions[0] != "low" {
		t.Fatal("levels alias the catalog slice")
	}
}
