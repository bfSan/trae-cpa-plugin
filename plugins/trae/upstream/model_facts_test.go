package upstream

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
)

func TestModelFactsPayableRateAndMalformedMetadata(t *testing.T) {
	dcc := `{"consumption_rate":{"data":{"rate":"0.2"}},"activity_discount":{"data":{"current":{"before_consumption_rate":2,"consumption_rate":0.2,"discount":10,"discount_type":"off_peak"},"off_peak":{"discount":10,"time_zone":"Asia/Shanghai","time_windows":[{"weekdays":[1,2,3,4,5],"start_minute":1380,"end_minute":480}]},"member":{"discount":0}}}}`
	f := parseModelFacts(json.RawMessage(dcc), 256000, json.RawMessage(`"v2"`))
	if f.Multiplier == nil || *f.Multiplier != 0.2 || f.BaseMultiplier == nil || *f.BaseMultiplier != 2 {
		t.Fatalf("payable rate changed: %+v", f)
	}
	if f.Promotions[0].Factor == nil || *f.Promotions[0].Factor != 0.1 {
		t.Fatal("discount 10 means payable 10 percent")
	}
	if f.Thinking != nil || f.ThinkingState != "unknown" {
		t.Fatal("invented thinking metadata")
	}
	if f.NewVersion != "v2" || f.Context != 256000 {
		t.Fatal("lost version/context")
	}
	w := f.Promotions[1].TimeWindows[0]
	if w.TimeZone != "Asia/Shanghai" || len(w.Weekdays) != 5 || *w.StartMinute != 1380 || *w.EndMinute != 480 {
		t.Fatalf("window lost: %+v", w)
	}
	if f.Promotions[2].Factor == nil || *f.Promotions[2].Factor != 0 {
		t.Fatal("lost free promotion")
	}
	for _, raw := range []string{`null`, `"not-json"`, `[]`, `{"consumption_rate":{"data":{"rate":"NaN"}}}`, `{"consumption_rate":{"data":{"rate":-1}}}`} {
		f := parseModelFacts(json.RawMessage(raw), 0, nil)
		if f.Multiplier != nil {
			t.Errorf("invalid rate became known: %s", raw)
		}
	}
}

func TestFetchModelsPreservesStringAndObjectFacts(t *testing.T) {
	metadata := `{"consumption_rate":{"data":{"rate":"0"}},"activity_discount":{"data":{"limited":{"end_at":2000000000,"discount":0}}}}`
	encoded, _ := json.Marshal(metadata)
	body := `{"config_info_list":[{"config_name":"a","display_config":{"display_name":"A"},"newversion":3,"display_contact_config":` + string(encoded) + `},{"config_name":"b","display_config":{"display_name":"B"},"display_contact_config":` + metadata + `},{"config_name":"c","display_config":{"display_name":"C"},"display_contact_config":"invalid"}]}`
	c := testClient(func(r *http.Request) (*http.Response, error) { return jsonResp(200, body), nil })
	models, err := c.FetchModels(&auth.Auth{AccessToken: "test", Variant: "cn"})
	if err != nil || len(models) != 3 {
		t.Fatalf("catalog failed on auxiliary facts: %v", err)
	}
	for _, m := range models[:2] {
		if m.Facts.Multiplier == nil || *m.Facts.Multiplier != 0 || len(m.Facts.Promotions) != 1 {
			t.Fatalf("lost zero or campaign: %+v", m)
		}
	}
	if models[0].Facts.NewVersion != "3" {
		t.Fatal("newversion lost")
	}
	if models[2].Facts.MetadataState != "unknown" {
		t.Fatal("malformed metadata not unknown")
	}
}

func TestFetchModelsSanitizedMetadataFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/model_metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	c := testClient(func(r *http.Request) (*http.Response, error) { return jsonResp(200, string(raw)), nil })
	models, err := c.FetchModels(&auth.Auth{AccessToken: "test", Variant: "cn"})
	if err != nil || len(models) != 3 {
		t.Fatalf("fixture parse: %v", err)
	}
	for _, m := range models {
		f := m.Facts
		if f.Multiplier == nil || f.ThinkingSupported == nil || !*f.ThinkingSupported || f.Thinking != nil || f.ThinkingState != "unknown" {
			t.Fatalf("lost real evidence or invented level: %+v", f)
		}
		for _, p := range f.Promotions {
			if p.Kind != "current" && p.Multiplier == nil {
				t.Fatal("after_consumption_rate discarded")
			}
		}
		if m.ID == "deepseek-v4.1-flash" {
			if f.Promotions[0].TimeZone != "Asia/Shanghai" || len(f.Promotions[0].TimeWindows) != 4 {
				t.Fatal("current observation failed to inherit named campaign schedule")
			}
		}
		if m.ID == "DeepSeek-V4-Flash-Official" {
			if f.Promotions[1].TimeZone != "" {
				t.Fatal("invented timezone")
			}
		}
	}
}

func TestModelFactsDisabledCampaignAndContextVersions(t *testing.T) {
	f := parseModelFacts(json.RawMessage(`{"consumption_rate":{"data":{"rate":1}},"activity_discount":{"enable":false,"data":{"current":{"consumption_rate":0.1,"discount":10}}}}`), 0, nil)
	if f.Multiplier == nil || *f.Multiplier != 1 || f.Promotions[0].Enabled == nil || *f.Promotions[0].Enabled {
		t.Fatal("disabled current overrode reported price")
	}
	contexts := parseModelContexts(json.RawMessage(`{"dev":200000,"max":1000000,"newversion":"256000","private_key":"not-model-metadata","bad":-1}`))
	if len(contexts) != 3 || contexts["newversion"] != 256000 || contexts["max"] != 1000000 {
		t.Fatal("context lanes lost or untrusted extras admitted")
	}
	f = parseModelFacts(json.RawMessage(`{"consumption_rate":{"data":{"rate":1}}}`), 0, nil)
	if f.PromotionsState != "unknown" {
		t.Fatal("missing activity data claimed no activities")
	}
}

func TestModelFactsMalformedScheduleDoesNotBreakModel(t *testing.T) {
	f := parseModelFacts(json.RawMessage(`{"activity_discount":{"data":{"off_peak":{"start_at":"not-a-time","time_windows":"wrong"},"limited":{"discount":"0","end_at":2000000000}}}}`), 0, nil)
	if len(f.Promotions) != 2 || !f.Promotions[1].ScheduleUnknown {
		t.Fatalf("expected unknown schedule: %+v", f)
	}
}
