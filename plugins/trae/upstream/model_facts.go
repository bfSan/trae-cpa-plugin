package upstream

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// ModelFacts contains only allowlisted model metadata, never auth or headers.
// Multiplier is already the payable rate: do NOT apply DiscountPercent again.
type ModelFacts struct {
	Multiplier         *float64         `json:"multiplier"`
	BaseMultiplier     *float64         `json:"base_multiplier"`
	ReportedMultiplier *float64         `json:"reported_multiplier"`
	Thinking           any              `json:"thinking"`
	ThinkingState      string           `json:"thinking_state"`
	ThinkingSupported  *bool            `json:"thinking_supported"`
	Context            int64            `json:"context,omitempty"`
	NewVersion         string           `json:"new_version,omitempty"`
	MetadataState      string           `json:"metadata_state"`
	PromotionsState    string           `json:"promotions_state"`
	Contexts           map[string]int64 `json:"contexts,omitempty"`
	Promotions         []ModelPromotion `json:"promotions"`
}

type ModelPromotion struct {
	Kind            string            `json:"kind"`
	Enabled         *bool             `json:"enabled"`
	DiscountType    string            `json:"discount_type,omitempty"`
	DiscountPercent *float64          `json:"discount_percent"`
	Factor          *float64          `json:"factor"`
	Multiplier      *float64          `json:"multiplier"`
	BaseMultiplier  *float64          `json:"base_multiplier"`
	StartAt         *int64            `json:"start_at"`
	EndAt           *int64            `json:"end_at"`
	TimeZone        string            `json:"time_zone,omitempty"`
	TimeWindows     []ModelTimeWindow `json:"time_windows"`
	ScheduleUnknown bool              `json:"schedule_unknown,omitempty"`
}

type ModelTimeWindow struct {
	Weekdays    []int  `json:"weekdays"`
	StartMinute *int   `json:"start_minute"`
	EndMinute   *int   `json:"end_minute"`
	TimeZone    string `json:"time_zone,omitempty"`
}

func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = json.RawMessage(text)
	}
	var out map[string]json.RawMessage
	_ = json.Unmarshal(raw, &out)
	return out
}
func factNumber(raw json.RawMessage) *float64 {
	s := strings.TrimSpace(string(raw))
	var text string
	if json.Unmarshal(raw, &text) == nil {
		s = text
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return nil
	}
	return &n
}
func factString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	if len(s) > 256 {
		return ""
	}
	return s
}
func factEpoch(raw json.RawMessage) *int64 {
	n := factNumber(raw)
	if n == nil || *n > 253402300799 || *n != math.Trunc(*n) {
		return nil
	}
	v := int64(*n)
	return &v
}
func factMinute(raw json.RawMessage) *int {
	n := factNumber(raw)
	if n == nil || *n > 1440 || *n != math.Trunc(*n) {
		return nil
	}
	v := int(*n)
	return &v
}
func modelVersion(raw json.RawMessage) string {
	if s := factString(raw); s != "" {
		return s
	}
	if n := factNumber(raw); n != nil {
		return strconv.FormatFloat(*n, 'f', -1, 64)
	}
	return ""
}

func parseModelContexts(raw json.RawMessage) map[string]int64 {
	out := map[string]int64{}
	for key, value := range rawObject(raw) {
		if key != "dev" && key != "max" && key != "newversion" {
			continue
		}
		if n := factNumber(value); n != nil && *n > 0 && *n < 1e10 && *n == math.Trunc(*n) {
			out[key] = int64(*n)
		}
	}
	return out
}

func parseModelFacts(raw json.RawMessage, context int64, version json.RawMessage) ModelFacts {
	f := ModelFacts{Context: context, NewVersion: modelVersion(version), ThinkingState: "unknown", MetadataState: "unknown", PromotionsState: "unknown", Promotions: []ModelPromotion{}}
	d := rawObject(raw)
	if d == nil {
		return f
	}
	f.MetadataState = "known"
	_ = json.Unmarshal(rawObject(d["reasoning"])["enable"], &f.ThinkingSupported)
	rate := rawObject(rawObject(d["consumption_rate"])["data"])
	f.ReportedMultiplier = factNumber(rate["rate"])
	f.Multiplier = f.ReportedMultiplier
	activityBlock := rawObject(d["activity_discount"])
	var activityEnabled *bool
	_ = json.Unmarshal(activityBlock["enable"], &activityEnabled)
	activity := rawObject(activityBlock["data"])
	if activity != nil {
		f.PromotionsState = "known"
	}
	for _, kind := range []string{"current", "limited", "off_peak", "subsidy", "member"} {
		node := rawObject(activity[kind])
		if len(node) == 0 {
			continue
		}
		p := ModelPromotion{Kind: kind, DiscountType: factString(node["discount_type"]), DiscountPercent: factNumber(node["discount"]), Multiplier: factNumber(node["consumption_rate"]), BaseMultiplier: factNumber(node["before_consumption_rate"]), StartAt: factEpoch(node["start_at"]), EndAt: factEpoch(node["end_at"]), TimeZone: factString(node["time_zone"]), TimeWindows: []ModelTimeWindow{}}
		p.Enabled = activityEnabled
		if rawEnabled, ok := node["enable"]; ok {
			var enabled *bool
			if json.Unmarshal(rawEnabled, &enabled) == nil {
				p.Enabled = enabled
			}
		}
		if kind != "current" && p.Multiplier == nil {
			p.Multiplier = factNumber(node["after_consumption_rate"])
		}
		if p.DiscountPercent != nil && *p.DiscountPercent <= 100 {
			v := *p.DiscountPercent / 100
			p.Factor = &v
		}
		for _, key := range []string{"start_at", "end_at"} {
			if val, ok := node[key]; ok && string(val) != "null" && factEpoch(val) == nil {
				p.ScheduleUnknown = true
			}
		}
		var windows []json.RawMessage
		if v, ok := node["time_windows"]; ok && string(v) != "null" {
			if json.Unmarshal(v, &windows) != nil {
				p.ScheduleUnknown = true
			}
		}
		if len(windows) > 32 {
			windows = windows[:32]
			p.ScheduleUnknown = true
		}
		for _, wraw := range windows {
			w := rawObject(wraw)
			tw := ModelTimeWindow{StartMinute: factMinute(w["start_minute"]), EndMinute: factMinute(w["end_minute"]), TimeZone: factString(w["time_zone"])}
			if v, ok := w["weekdays"]; ok && string(v) != "null" {
				if json.Unmarshal(v, &tw.Weekdays) != nil {
					p.ScheduleUnknown = true
				}
			}
			if tw.TimeZone == "" {
				tw.TimeZone = p.TimeZone
			}
			p.TimeWindows = append(p.TimeWindows, tw)
		}
		if kind == "current" && (p.Enabled == nil || *p.Enabled) {
			if p.Multiplier != nil {
				f.Multiplier = p.Multiplier
			}
			f.BaseMultiplier = p.BaseMultiplier
		}
		f.Promotions = append(f.Promotions, p)
	}
	// The current observation inherits only the explicitly named campaign's
	// schedule. This lets consumers invalidate that observation at boundaries.
	for i := range f.Promotions {
		p := &f.Promotions[i]
		if p.Kind != "current" {
			continue
		}
		for _, campaign := range f.Promotions {
			if campaign.Kind == p.DiscountType && campaign.Kind != "current" {
				p.StartAt, p.EndAt = campaign.StartAt, campaign.EndAt
				p.TimeZone, p.TimeWindows = campaign.TimeZone, campaign.TimeWindows
				p.ScheduleUnknown = p.ScheduleUnknown || campaign.ScheduleUnknown
			}
		}
	}
	return f
}
