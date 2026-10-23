package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
)

// All responses are in-memory RoundTripper fixtures: no chat or real HTTP.
func TestDeclaredContextPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, primary, auxiliary string
		want                     int64
	}{
		{"duplicate 256k dev max", `{"dev":256000,"max":256000}`, `{"dev":256000,"max":256000}`, 256000},
		{"non round 981504", `{"dev":200000}`, `{"dev":200000,"max":981504}`, 981504},
		{"one million", `{"dev":200000}`, `{"max":1000000}`, 1000000},
		{"not hardcoded million", `{"dev":200000}`, `{"max":1500000}`, 1500000},
		{"missing max keeps primary", `{"dev":200000}`, `{"dev":981504,"newversion":1000000}`, 200000},
		{"primary max", `{"dev":200000,"max":981504}`, `{"dev":256000}`, 981504},
		{"maximum across sources", `{"dev":200000,"max":1000000}`, `{"max":981504}`, 1000000},
		{"max array", `{"dev":200000}`, `{"max":[400000,1000000,400000,981504]}`, 1000000},
		{"primary array", `{"dev":200000,"max":[981504,256000]}`, `{}`, 981504},
		{"dev array is not max", `{"dev":200000}`, `{"dev":[256000,1000000]}`, 200000},
		{"max smaller than primary", `{"dev":256000}`, `{"max":200000}`, 256000},
		{"invalid max array", `{"dev":200000}`, `{"max":[1000000,-1]}`, 200000},
		{"fraction max", `{"dev":200000}`, `{"max":981504.5}`, 200000},
		{"absurd max", `{"dev":200000}`, `{"max":100000001}`, 200000},
		{"string max", `{"dev":200000}`, `{"max":"981504"}`, 981504},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := testClient(func(r *http.Request) (*http.Response, error) {
				calls++
				contexts := tc.primary
				if calls == 2 {
					contexts = tc.auxiliary
				}
				return jsonResp(200, `{"config_info_list":[{"config_name":"m","display_config":{"display_name":"M"},"context_window_tokens":`+contexts+`,"prompt_max_tokens":9000000,"max_tokens":8000000}]}`), nil
			})
			models, err := c.FetchModels(&auth.Auth{Variant: "cn", AccessToken: "account-a"})
			if err != nil || len(models) != 1 || models[0].ContextWindow != tc.want {
				t.Fatalf("models=%+v err=%v want=%d", models, err, tc.want)
			}
			m := models[0]
			if calls != 2 || m.MaxTokens != 0 || m.Facts.ContextEvidence != nil {
				t.Fatalf("policy changed output, synthesized evidence, or retried: %+v calls=%d", m, calls)
			}
			if m.Facts.Context != m.Facts.Contexts["dev"] {
				t.Fatal("primary declaration was overwritten")
			}
		})
	}
}

func TestDeclaredContextRequiresExactVisibleIntersection(t *testing.T) {
	for _, tc := range []struct {
		name, rows string
	}{
		{"case mismatch", `{"config_name":"M","display_config":{"display_name":"M"},"context_window_tokens":{"max":1000000}}`},
		{"other id", `{"config_name":"agent-only","display_config":{"display_name":"M"},"context_window_tokens":{"max":1000000}}`},
		{"disabled", `{"config_name":"m","config_switch":false,"display_config":{"display_name":"M"},"context_window_tokens":{"max":1000000}}`},
		{"hidden", `{"config_name":"m","is_invisible_to_user":true,"display_config":{"display_name":"M"},"context_window_tokens":{"max":1000000}}`},
		{"max mode false", `{"config_name":"m","display_config":{"display_name":"M","max_mode":false},"context_window_tokens":{"max":1000000}}`},
		{"ambiguous duplicate", `{"config_name":"m","display_config":{"display_name":"M"},"context_window_tokens":{"max":1000000}},{"config_name":"m","display_config":{"display_name":"M"},"context_window_tokens":{"max":981504}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := testClient(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return jsonResp(200, `{"config_info_list":[{"config_name":"m","display_config":{"display_name":"M","max_mode":false},"context_window_tokens":{"dev":200000,"max":2000000}}]}`), nil
				}
				return jsonResp(200, `{"config_info_list":[`+tc.rows+`]}`), nil
			})
			models, err := c.FetchModels(&auth.Auth{Variant: "cn"})
			if err != nil || len(models) != 1 || models[0].ID != "m" || models[0].ContextWindow != 200000 {
				t.Fatalf("untrusted auxiliary expanded authorization: %+v %v", models, err)
			}
		})
	}
}

func TestDeclaredContextPrimaryMaxModeFalseDoesNotVetoAuxiliary(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(200, `{"config_info_list":[{"config_name":"deepseek-v4.1-flash","display_config":{"display_name":"Deepseek","max_mode":false},"context_window_tokens":{"dev":200000,"max":2000000}}]}`), nil
		}
		return jsonResp(200, `{"config_info_list":[{"config_name":"deepseek-v4.1-flash","display_config":{"display_name":"Deepseek","max_mode":true},"context_window_tokens":{"dev":200000,"max":1000000}}]}`), nil
	})
	models, err := c.FetchModels(&auth.Auth{Variant: "cn"})
	if err != nil || len(models) != 1 || models[0].ContextWindow != 1000000 || models[0].Facts.ContextEvidence != nil {
		t.Fatalf("max_mode must remain source-scoped: %+v %v", models, err)
	}
}

func TestDeclaredContextPartialAuxiliaryRetainsPrimaryMax(t *testing.T) {
	for _, withMax := range []bool{false, true} {
		calls := 0
		c := testClient(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 2 {
				return jsonResp(503, `{}`), nil
			}
			contexts := `{"dev":256000}`
			if withMax {
				contexts = `{"dev":256000,"max":981504}`
			}
			return jsonResp(200, `{"config_info_list":[{"config_name":"m","display_config":{"display_name":"M"},"context_window_tokens":`+contexts+`}]}`), nil
		})
		models, err := c.FetchModels(&auth.Auth{Variant: "cn"})
		want := int64(256000)
		if withMax {
			want = 981504
		}
		if err != nil || calls != 2 || len(models) != 1 || models[0].ContextWindow != want || models[0].Facts.CapabilityStatus != "partial" {
			t.Fatalf("partial response=%+v err=%v calls=%d", models, err, calls)
		}
	}
}

func TestDeclaredContextAccountAndRealmIsolation(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Function string `json:"function"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		contexts := `{"dev":200000}`
		if body.Function == "solo_agent" && r.Header.Get("Authorization") == "Cloud-IDE-JWT account-a" {
			contexts = `{"max":1000000}`
		}
		return jsonResp(200, `{"config_info_list":[{"config_name":"m","display_config":{"display_name":"M"},"context_window_tokens":`+contexts+`}]}`), nil
	})
	for _, token := range []string{"account-a", "account-b", "account-a"} {
		models, err := c.FetchModels(&auth.Auth{Variant: "cn", AccessToken: token})
		want := int64(200000)
		if token == "account-a" {
			want = 1000000
		}
		if err != nil || len(models) != 1 || models[0].ContextWindow != want {
			t.Fatalf("account=%s models=%+v err=%v", token, models, err)
		}
	}
	for _, realm := range []string{"cn", "solo", "intl"} {
		models := []ModelInfo{{ID: "m", ContextWindow: 200000, Facts: ModelFacts{CapabilityCatalog: []ModelCapability{{SourceFunction: "solo_agent", ContextOptions: map[string][]int64{"max": {256000, 981504}}}}}}}
		applyDeclaredModelContexts(&auth.Auth{Variant: realm}, models)
		want := int64(981504)
		if realm == "intl" {
			want = 200000
		}
		if models[0].ContextWindow != want {
			t.Fatalf("realm %s: %+v", realm, models)
		}
	}
}
