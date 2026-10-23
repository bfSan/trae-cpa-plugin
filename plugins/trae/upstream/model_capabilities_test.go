package upstream

import (
	"encoding/json"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCapabilityCatalogEnrichesWithoutExpandingAuthorization(t *testing.T) {
	calls := []string{}
	c := testClient(func(r *http.Request) (*http.Response, error) {
		var b map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &b)
		f, _ := b["function"].(string)
		calls = append(calls, f)
		if f == "solo_work_lite" {
			return jsonResp(200, `{"config_info_list":[{"config_name":"m","display_config":{"display_name":"M"},"context_window_tokens":{"dev":200000},"reasoning_effort_config":{"options":["low","high"],"default_level":"high"}}]}`), nil
		}
		if f != "solo_agent" {
			t.Fatalf("unexpected function %s", f)
		}
		return jsonResp(200, `{"config_info_list":[{"config_name":"m","display_config":{"display_name":"M","max_mode":true},"context_window_tokens":{"dev":200000,"max":1000000},"prompt_max_tokens":936000,"max_tokens":null,"reasoning_effort_config":{"options":["high","extra_high"],"default_level":"extra_high"}},{"config_name":"agent-only","display_config":{"display_name":"Agent"},"context_window_tokens":{"max":1000000}}]}`), nil
	})
	got, err := c.FetchModels(&auth.Auth{Variant: "cn", AccessToken: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "solo_work_lite" || calls[1] != "solo_agent" {
		t.Fatalf("requests=%v", calls)
	}
	if len(got) != 1 || got[0].ID != "m" || got[0].ContextWindow != 1000000 {
		t.Fatalf("wrong authorized model or declared context: %+v", got)
	}
	facts := got[0].Facts
	if len(facts.CapabilityCatalog) != 2 {
		t.Fatal("lost catalog sources")
	}
	aux := facts.CapabilityCatalog[1]
	if aux.Contexts["max"] != 1000000 || aux.PromptMaxTokens == nil || *aux.PromptMaxTokens != 936000 || aux.OutputMaxTokens != nil {
		t.Fatalf("conflated limits: %+v", aux)
	}
	if aux.ThinkingDefault != "extra_high" || facts.Thinking != "high" {
		t.Fatal("auxiliary default leaked into primary lane")
	}
}
func TestCapabilityFailurePreservesPrimaryAndDoesNotRetry(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(200, `{"config_info_list":[{"config_name":"m","display_config":{"display_name":"M"},"context_window_tokens":{"dev":200000}}]}`), nil
		}
		return jsonResp(503, `{}`), nil
	})
	got, e := c.FetchModels(&auth.Auth{Variant: "cn", AccessToken: "mock"})
	if e != nil || len(got) != 1 || calls != 2 || got[0].Facts.CapabilityStatus != "partial" {
		t.Fatalf("partial failure mishandled: %+v %v calls=%d", got, e, calls)
	}
}
func TestCapabilityNumericAndMultiWindowParsing(t *testing.T) {
	var m catalogModel
	if e := json.Unmarshal([]byte(`{"config_name":"m","context_window_tokens":{"dev":[200000,256000],"max":[1000000,400000,400000]},"prompt_max_tokens":936000,"max_tokens":null,"reasoning_effort_config":{"options":[{"level":"low"},{"level":"extra_high"}],"default_level":"not-reported"}}`), &m); e != nil {
		t.Fatal(e)
	}
	c := modelCapability(m, "solo_agent")
	if c.Contexts["dev"] != 0 || len(c.ContextOptions["dev"]) != 2 || c.Contexts["max"] != 1000000 || len(c.ContextOptions["max"]) != 2 {
		t.Fatalf("guessed dev default or lost sizes: %+v", c)
	}
	if c.OutputMaxTokens != nil || c.ThinkingDefault != "" || len(c.ThinkingOptions) != 2 {
		t.Fatalf("guessed unsupported limit/default %+v", c)
	}
}

func TestCapabilityCatalogLargerThanGenericJSONLimit(t *testing.T) {
	large := `{"ignored_encrypted_detail":"` + strings.Repeat("a", (1<<20)+64) + `","config_info_list":[{"config_name":"m","display_config":{"display_name":"M","max_mode":true},"context_window_tokens":{"dev":200000,"max":1000000}}]}`
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(200, `{"config_info_list":[{"config_name":"m","display_config":{"display_name":"M"},"context_window_tokens":{"dev":200000}}]}`), nil
		}
		return jsonResp(200, large), nil
	})
	got, err := c.FetchModels(&auth.Auth{Variant: "cn", AccessToken: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Facts.CapabilityStatus != "complete" || len(got[0].Facts.CapabilityCatalog) != 2 || got[0].Facts.CapabilityCatalog[1].Contexts["max"] != 1000000 {
		t.Fatal("large catalog was truncated")
	}
}
func TestCapabilityOversizeIsExplicitPartialNotSilentTruncation(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(200, `{"config_info_list":[{"config_name":"m","display_config":{"display_name":"M"}}]}`), nil
		}
		return jsonResp(200, strings.Repeat("x", maxModelCatalogBytes+1)), nil
	})
	got, err := c.FetchModels(&auth.Auth{Variant: "cn", AccessToken: "mock"})
	if err != nil || len(got) != 1 || got[0].Facts.CapabilityStatus != "partial" || calls != 2 {
		t.Fatal("oversize catalog was accepted or retried")
	}
}
