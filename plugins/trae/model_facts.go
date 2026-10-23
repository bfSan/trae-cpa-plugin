package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
)

// Observation validity, not a billing guarantee. No refresh changes scheduling.
const modelFactsMaxAge = 24 * time.Hour

func modelSnapshotStale(fetched string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, fetched)
	return err != nil || t.After(now.Add(time.Minute)) || now.Sub(t) > modelFactsMaxAge
}
func sameModelFacts(a, b map[string]upstream.ModelFacts) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

type accountModelFacts struct {
	upstream.ModelFacts
	AccountScope        string `json:"account_scope"`
	FetchedAt           string `json:"fetched_at,omitempty"`
	Stale               bool   `json:"stale"`
	RateStale           bool   `json:"rate_stale"`
	RateScheduleUnknown bool   `json:"rate_schedule_unknown"`
}
type modelPromotionView struct {
	upstream.ModelPromotion
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Realm         string   `json:"realm"`
	ModelID       string   `json:"model_id"`
	Models        []string `json:"models"`
	AccountScope  string   `json:"account_scope"`
	Status        string   `json:"status"`
	StatusReason  string   `json:"status_reason,omitempty"`
	FetchedAt     string   `json:"fetched_at,omitempty"`
	SnapshotStale bool     `json:"snapshot_stale"`
	NextChange    string   `json:"next_change,omitempty"`
}
type modelPromotionsResponse struct {
	Status     string               `json:"status"`
	ServerTime string               `json:"server_time"`
	Items      []modelPromotionView `json:"items"`
}

func promotionStatus(p upstream.ModelPromotion, fetched string, stale bool, now time.Time) (string, string, string) {
	if p.EndAt != nil && now.Unix() >= *p.EndAt {
		return "expired", "", ""
	}
	if stale {
		return "unknown", "snapshot_stale", ""
	}
	if p.Enabled != nil && !*p.Enabled {
		return "inactive", "upstream_disabled", ""
	}
	if p.Kind == "current" && p.DiscountType == "none" {
		return "inactive", "upstream_no_discount", ""
	}
	if p.ScheduleUnknown {
		return "unknown", "unsupported_schedule", ""
	}
	if p.StartAt != nil && p.EndAt != nil && *p.EndAt <= *p.StartAt {
		return "unknown", "invalid_bounds", ""
	}
	if p.StartAt != nil && now.Unix() < *p.StartAt {
		return "upcoming", "", time.Unix(*p.StartAt, 0).UTC().Format(time.RFC3339)
	}
	next := ""
	if p.EndAt != nil {
		next = time.Unix(*p.EndAt, 0).UTC().Format(time.RFC3339)
	}
	if len(p.TimeWindows) > 0 {
		active, known := promotionWindowMatch(p.TimeWindows, now)
		if !known {
			return "unknown", "unsupported_timezone_or_weekdays", next
		}
		// A bounded minute scan respects named zones, weekdays, DST and midnight.
		// It computes the next transition without freezing today's state in storage.
		for t := now.Truncate(time.Minute).Add(time.Minute); t.Before(now.Add(8 * 24 * time.Hour)); t = t.Add(time.Minute) {
			a, k := promotionWindowMatch(p.TimeWindows, t)
			if k && a != active {
				candidate := t.UTC().Format(time.RFC3339)
				if next == "" || candidate < next {
					next = candidate
				}
				break
			}
		}
		if active {
			return "active", "", next
		}
		return "inactive", "outside_window", next
	}
	if p.Kind == "current" {
		observed, err := time.Parse(time.RFC3339, fetched)
		if err != nil || now.Sub(observed) > modelFactsMaxAge {
			return "unknown", "snapshot_stale", next
		}
		return "active", "upstream_current_at_snapshot", next
	}
	if p.StartAt != nil || p.EndAt != nil {
		return "active", "within_campaign_bounds", next
	}
	return "unknown", "missing_schedule", next
}

// The sanitized get_detail_param evidence uses ISO weekdays 1..5 and 6,7.
// An overnight window belongs to its starting local weekday. No timezone is
// inferred from realm/browser/server; absent or unsupported zones stay unknown.
var modelTimeZones sync.Map

func modelTimeZone(name string) (*time.Location, error) {
	if cached, ok := modelTimeZones.Load(name); ok {
		return cached.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err == nil {
		modelTimeZones.Store(name, loc)
	}
	return loc, err
}
func promotionWindowMatch(windows []upstream.ModelTimeWindow, now time.Time) (bool, bool) {
	unknown := false
	for _, w := range windows {
		if w.TimeZone == "" || w.StartMinute == nil || w.EndMinute == nil || *w.StartMinute >= 1440 || *w.EndMinute > 1440 || *w.StartMinute == *w.EndMinute {
			unknown = true
			continue
		}
		loc, err := modelTimeZone(w.TimeZone)
		if err != nil {
			unknown = true
			continue
		}
		local := now.In(loc)
		minute := local.Hour()*60 + local.Minute()
		start, end := *w.StartMinute, *w.EndMinute
		day := int(local.Weekday())
		if day == 0 {
			day = 7
		}
		if start > end && minute < end {
			day--
			if day == 0 {
				day = 7
			}
		}
		if len(w.Weekdays) > 0 {
			matches, valid := false, true
			for _, d := range w.Weekdays {
				if d < 1 || d > 7 {
					valid = false
				}
				if d == day {
					matches = true
				}
			}
			if !valid {
				unknown = true
				continue
			}
			if !matches {
				continue
			}
		}
		if (start < end && minute >= start && minute < end) || (start > end && (minute >= start || minute < end)) {
			return true, true
		}
	}
	return false, !unknown
}

// A saved current price cannot survive a known campaign transition. Never
// recalculate it from discounts (membership/subsidy selection is account-owned).
func modelRateBoundaryPassed(f upstream.ModelFacts, fetched string, now time.Time) bool {
	observed, err := time.Parse(time.RFC3339, fetched)
	if err != nil {
		return true
	}
	for _, p := range f.Promotions {
		for _, boundary := range []*int64{p.StartAt, p.EndAt} {
			if boundary != nil && *boundary > observed.Unix() && *boundary <= now.Unix() {
				return true
			}
		}
		if len(p.TimeWindows) == 0 {
			continue
		}
		_, _, next := promotionStatus(p, fetched, false, observed)
		if next != "" {
			t, err := time.Parse(time.RFC3339, next)
			if err == nil && !now.Before(t) {
				return true
			}
		}
	}
	return false
}

func enrichModelGroup(g *modelGroup, entries []*variantCatalogEntry, now time.Time) {
	for i := range g.Models {
		row := &g.Models[i]
		row.Multiplier = nil
		row.ThinkingState = "unknown"
		row.ContextState = "unknown"
		if len(row.ContextLengths) > 1 {
			row.ContextState = "different"
		}
		row.MultiplierState = "unknown"
		row.Facts = []accountModelFacts{}
		row.Multipliers = []float64{}
		if row.ContextLength > 0 {
			v := row.ContextLength
			row.Context = &v
		}
		known, unknown, stale := 0, false, false
		contextKnown := 0
		for ei, e := range entries {
			if e.snapshot == nil {
				unknown = true
				row.UnknownCredentials++
				continue
			}
			present := false
			for _, m := range e.snapshot.Models {
				if m.ID == row.ID {
					present = true
					if m.ContextLength > 0 {
						contextKnown++
					}
					break
				}
			}
			if !present {
				continue
			}
			f, ok := e.snapshot.Facts[row.ID]
			if !ok {
				f = upstream.ModelFacts{ThinkingState: "unknown", MetadataState: "unknown", Promotions: []upstream.ModelPromotion{}}
			}
			// Only the serving lane's explicit options/default are authoritative.
			f.Thinking = nil
			f.ThinkingState = "unknown"
			if d := primaryThinkingDefault(f, upstream.FunctionFor(g.Realm)); d != "" {
				f.Thinking = d
				f.ThinkingState = "known"
			}
			af := accountModelFacts{ModelFacts: f, AccountScope: fmt.Sprintf("%s-account-%d", g.Realm, ei+1), FetchedAt: e.snapshot.FetchedAt, Stale: modelSnapshotStale(e.snapshot.FetchedAt, now)}
			af.RateStale = af.Stale || modelRateBoundaryPassed(f, e.snapshot.FetchedAt, now)
			for _, p := range f.Promotions {
				if p.Enabled != nil && !*p.Enabled {
					continue
				}
				if p.ScheduleUnknown {
					af.RateScheduleUnknown = true
				}
				if len(p.TimeWindows) > 0 {
					_, known := promotionWindowMatch(p.TimeWindows, now)
					if !known {
						af.RateScheduleUnknown = true
					}
				}
			}
			unknown = unknown || af.RateScheduleUnknown
			row.Facts = append(row.Facts, af)
			stale = stale || af.RateStale
			if f.Multiplier == nil {
				unknown = true
				continue
			}
			known++
			found := false
			for _, n := range row.Multipliers {
				if n == *f.Multiplier {
					found = true
				}
			}
			if !found {
				row.Multipliers = append(row.Multipliers, *f.Multiplier)
			}
		}
		if contextKnown != row.Credentials || row.UnknownCredentials > 0 {
			row.Context = nil
			row.ContextLength = 0
		} else if row.Context != nil {
			row.ContextState = "known"
		}
		aggregateThinkingDefault(row)
		sort.Float64s(row.Multipliers)
		switch {
		case len(row.Multipliers) > 1:
			row.MultiplierState = "different"
		case stale:
			row.MultiplierState = "stale"
		case known > 0 && !unknown && known == row.Credentials:
			row.MultiplierState = "known"
			v := row.Multipliers[0]
			row.Multiplier = &v
		}
	}
}
func promotionsForGroups(groups []modelGroup, now time.Time) modelPromotionsResponse {
	out := modelPromotionsResponse{Status: "unknown", ServerTime: now.UTC().Format(time.RFC3339), Items: []modelPromotionView{}}
	evidence, missing := false, false
	for _, g := range groups {
		if g.Credentials > 0 && len(g.Models) == 0 {
			missing = true
		}
		for _, m := range g.Models {
			if m.UnknownCredentials > 0 {
				missing = true
			}
			for _, f := range m.Facts {
				if f.PromotionsState == "known" && !f.Stale {
					evidence = true
				} else {
					missing = true
				}
				for pi, p := range f.Promotions {
					// Ordinary current pricing is evidence, not a campaign card.
					if p.Kind == "current" && (p.DiscountType == "none" || (p.DiscountPercent != nil && *p.DiscountPercent >= 100) || (p.Multiplier != nil && p.BaseMultiplier != nil && *p.Multiplier >= *p.BaseMultiplier)) {
						continue
					}
					status, reason, next := promotionStatus(p, f.FetchedAt, f.Stale, now)
					if p.Kind == "current" && f.RateStale && !f.Stale {
						status, reason = "unknown", "rate_observation_crossed_boundary"
					}
					out.Items = append(out.Items, modelPromotionView{ModelPromotion: p, ID: fmt.Sprintf("%s:%s:%d", f.AccountScope, m.ID, pi), Title: p.Kind, Realm: g.Realm, ModelID: m.ID, Models: []string{m.ID}, AccountScope: f.AccountScope, Status: status, StatusReason: reason, FetchedAt: f.FetchedAt, SnapshotStale: f.Stale, NextChange: next})
				}
			}
		}
	}
	if evidence && !missing {
		out.Status = "known"
	}
	return out
}

// Refresh must reread ONLY this account's file: discovery may have failed and
// returned old models, which does not make its metadata fresh.
func reloadCatalogSnapshot(e *variantCatalogEntry) {
	_, raw, ok := locateCredentialFile(e.storage)
	if ok {
		if c := readModelCacheDoc(raw); c != nil && c.Realm == e.variant {
			e.snapshot = c
		}
	}
}

func primaryThinkingDefault(f upstream.ModelFacts, function string) string {
	for _, c := range f.CapabilityCatalog {
		if c.SourceFunction != function || c.ThinkingDefault == "" {
			continue
		}
		for _, option := range c.ThinkingOptions {
			if option == c.ThinkingDefault {
				return option
			}
		}
	}
	return ""
}
func aggregateThinkingDefault(row *modelGroupEntry) {
	row.Thinking = nil
	row.ThinkingState = "unknown"
	if row.UnknownCredentials > 0 || len(row.Facts) != row.Credentials || row.Credentials == 0 {
		return
	}
	current := ""
	for _, f := range row.Facts {
		if f.Stale {
			row.ThinkingState = "stale"
			return
		}
		d, ok := f.Thinking.(string)
		if !ok || d == "" || f.ThinkingState != "known" {
			return
		}
		if current != "" && current != d {
			row.ThinkingState = "different"
			return
		}
		current = d
	}
	row.Thinking = current
	row.ThinkingState = "known"
}
