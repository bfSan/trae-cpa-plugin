package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
)

func factFloat(n float64) *float64 { return &n }
func factInt(n int) *int           { return &n }
func factUnix(n int64) *int64      { return &n }

func TestPromotionClockAndUnknownSchedules(t *testing.T) {
	now := time.Date(2026, 10, 22, 16, 30, 0, 0, time.UTC) // Shanghai Friday 00:30
	fetched := now.Add(-time.Minute).Format(time.RFC3339)
	window := upstream.ModelTimeWindow{TimeZone: "Asia/Shanghai", StartMinute: factInt(1380), EndMinute: factInt(480), Weekdays: []int{1, 2, 3, 4, 5, 6, 7}}
	p := upstream.ModelPromotion{Kind: "off_peak", TimeWindows: []upstream.ModelTimeWindow{window}}
	status, _, next := promotionStatus(p, fetched, false, now)
	if status != "active" || next != "2026-10-23T00:00:00Z" {
		t.Fatalf("midnight/timezone: %s %s", status, next)
	}
	p.TimeWindows[0].TimeZone = ""
	if s, _, _ := promotionStatus(p, fetched, false, now); s != "unknown" {
		t.Fatal("guessed missing timezone")
	}
	p.TimeWindows[0] = window
	p.TimeWindows[0].Weekdays = []int{1, 2, 3, 4, 5}
	if s, _, _ := promotionStatus(p, fetched, false, now); s != "active" {
		t.Fatal("ISO Thursday overnight window lost")
	}
	p.TimeWindows[0].Weekdays = []int{5}
	if s, _, _ := promotionStatus(p, fetched, false, now); s != "inactive" {
		t.Fatal("overnight incorrectly assigned to ending weekday")
	}
	p.TimeWindows[0].Weekdays = []int{0}
	if s, _, _ := promotionStatus(p, fetched, false, now); s != "unknown" {
		t.Fatal("unsupported weekday silently guessed")
	}
	p.TimeWindows = nil
	p.StartAt = factUnix(now.Add(time.Hour).Unix())
	if s, _, _ := promotionStatus(p, fetched, false, now); s != "upcoming" {
		t.Fatal("future active")
	}
	p.EndAt = factUnix(now.Add(-time.Minute).Unix())
	p.StartAt = nil
	if s, _, _ := promotionStatus(p, fetched, true, now); s != "expired" {
		t.Fatal("expired campaign revived")
	}
	p = upstream.ModelPromotion{Kind: "current", Multiplier: factFloat(0.1)}
	if s, _, _ := promotionStatus(p, fetched, true, now); s != "unknown" {
		t.Fatal("stale current rate treated as live")
	}
	if s, _, _ := promotionStatus(p, fetched, false, now); s != "active" {
		t.Fatal("fresh current evidence missing")
	}
	p.Kind = "member"
	if s, _, _ := promotionStatus(p, fetched, false, now); s != "unknown" {
		t.Fatal("ungated membership inferred")
	}
}

func TestAccountFactsDifferencesUnknownAndStale(t *testing.T) {
	now := time.Now().UTC()
	fetched := now.Format(time.RFC3339)
	entry := func(rate *float64, at string) *variantCatalogEntry {
		return &variantCatalogEntry{variant: "cn", snapshot: &persistedModelCache{Realm: "cn", FetchedAt: at, Models: variantTestModels("m"), Facts: map[string]upstream.ModelFacts{"m": {Multiplier: rate, ThinkingState: "unknown", MetadataState: "known", Promotions: []upstream.ModelPromotion{}}}}}
	}
	a, b := entry(factFloat(0.2), fetched), entry(factFloat(0.3), fetched)
	a.name = "a"
	b.name = "b"
	g := buildModelGroup("cn", []*variantCatalogEntry{a, b}, false)
	row := g.Models[0]
	if row.Multiplier != nil || row.MultiplierState != "different" || len(row.Facts) != 2 || len(row.Multipliers) != 2 {
		t.Fatalf("borrowed first account rate: %+v", row)
	}
	b.snapshot.Facts["m"] = a.snapshot.Facts["m"]
	g = buildModelGroup("cn", []*variantCatalogEntry{a, b}, false)
	if g.Models[0].Multiplier == nil || *g.Models[0].Multiplier != 0.2 {
		t.Fatal("lost agreeing account evidence")
	}
	missingGroup := buildModelGroup("cn", []*variantCatalogEntry{a, {variant: "cn", name: "never-fetched"}}, false)
	if missingGroup.Models[0].Multiplier != nil || missingGroup.Models[0].UnknownCredentials != 1 {
		t.Fatal("first account represented an unfetched region peer")
	}
	b.snapshot.Facts = nil
	g = buildModelGroup("cn", []*variantCatalogEntry{a, b}, false)
	if g.Models[0].Multiplier != nil || g.Models[0].MultiplierState != "unknown" {
		t.Fatal("unknown peer treated as equal")
	}
	b.snapshot.Facts = a.snapshot.Facts
	b.snapshot.FetchedAt = now.Add(-modelFactsMaxAge - time.Hour).Format(time.RFC3339)
	g = buildModelGroup("cn", []*variantCatalogEntry{a, b}, false)
	if g.Models[0].Multiplier != nil || g.Models[0].MultiplierState != "stale" {
		t.Fatal("stale rate projected as current")
	}
	payload, _ := json.Marshal(g)
	if strings.Contains(string(payload), "accessToken") || strings.Contains(string(payload), "high") {
		t.Fatal("secrets or invented thinking in facts")
	}
}

func TestPersistFactsChangesAndWatcherConvergence(t *testing.T) {
	storage := persistTestStorage("test-current", "cn", `,"note":"preserve"`)
	files := map[string][]byte{"self": storage}
	defer stubPersistSeams(t, files)()
	dynamic := []upstream.ModelInfo{{ID: "m", Facts: upstream.ModelFacts{Multiplier: factFloat(0.2), MetadataState: "known", NewVersion: "v1"}}}
	models := variantTestModels("m")
	persistModelSnapshotWithFacts(storage, "cn", models, dynamic)
	persistModelSnapshotWithFacts(storage, "cn", models, dynamic)
	if len(saveCalls()) != 1 {
		t.Fatalf("metadata watcher loop: %d saves", len(saveCalls()))
	}
	dynamic[0].Facts.Multiplier = factFloat(0.1)
	dynamic[0].Facts.NewVersion = "v2"
	persistModelSnapshotWithFacts(storage, "cn", models, dynamic)
	if len(saveCalls()) != 2 {
		t.Fatal("metadata-only change dropped")
	}
	cache := readModelCacheDoc(files["self"])
	if cache.Facts["m"].NewVersion != "v2" || *cache.Facts["m"].Multiplier != 0.1 {
		t.Fatal("facts not persisted")
	}
	if !strings.Contains(string(files["self"]), `"note":"preserve"`) {
		t.Fatal("unrelated doc field lost")
	}
	// Re-observation renews only after expiry, not at each watcher callback.
	cache.FetchedAt = time.Now().Add(-modelFactsMaxAge - time.Hour).UTC().Format(time.RFC3339)
	files["self"], _ = mergeModelCacheIntoDoc(files["self"], cache)
	persistModelSnapshotWithFacts(storage, "cn", models, dynamic)
	if modelSnapshotStale(readModelCacheDoc(files["self"]).FetchedAt, time.Now()) {
		t.Fatal("unchanged facts never renewed")
	}
	if len(saveCalls()) != 3 {
		t.Fatal("expected one renewal")
	}
}

func TestObservedRateInvalidatedAtScheduleBoundary(t *testing.T) {
	observed := time.Date(2026, 10, 23, 0, 50, 0, 0, time.UTC) // Shanghai 08:50
	f := upstream.ModelFacts{Multiplier: factFloat(0.06), Promotions: []upstream.ModelPromotion{{Kind: "off_peak", TimeWindows: []upstream.ModelTimeWindow{{TimeZone: "Asia/Shanghai", StartMinute: factInt(0), EndMinute: factInt(540), Weekdays: []int{1, 2, 3, 4, 5}}}}}}
	if modelRateBoundaryPassed(f, observed.Format(time.RFC3339), observed.Add(5*time.Minute)) {
		t.Fatal("rate invalidated before boundary")
	}
	if !modelRateBoundaryPassed(f, observed.Format(time.RFC3339), observed.Add(10*time.Minute)) {
		t.Fatal("rate observation outlived 09:00 boundary")
	}
	p := upstream.ModelPromotion{Kind: "current", DiscountType: "none", DiscountPercent: factFloat(100)}
	if status, _, _ := promotionStatus(p, observed.Format(time.RFC3339), false, observed); status != "inactive" {
		t.Fatal("no discount rendered active")
	}
}

func TestPromotionEnvelopeUnknownVersusKnownEmpty(t *testing.T) {
	now := time.Now()
	if got := promotionsForGroups(nil, now); got.Status != "unknown" || got.Items == nil {
		t.Fatal("empty treated as no campaigns")
	}
	groups := []modelGroup{{Models: []modelGroupEntry{{Facts: []accountModelFacts{{ModelFacts: upstream.ModelFacts{MetadataState: "known", PromotionsState: "known"}}}}}}}
	if got := promotionsForGroups(groups, now); got.Status != "known" {
		t.Fatal("known empty metadata not retained")
	}
	groups[0].Models[0].Facts[0].Promotions = []upstream.ModelPromotion{{Kind: "current", DiscountType: "none"}, {Kind: "current", DiscountPercent: factFloat(100)}, {Kind: "current", Multiplier: factFloat(1), BaseMultiplier: factFloat(1)}, {Kind: "member", DiscountPercent: factFloat(50)}}
	if got := promotionsForGroups(groups, now); len(got.Items) != 1 || got.Items[0].Kind != "member" {
		t.Fatal("ordinary prices polluted campaign list")
	}
	groups[0].Models[0].Facts[0].Stale = true
	if got := promotionsForGroups(groups, now); got.Status != "unknown" {
		t.Fatal("stale envelope claims known")
	}
}
