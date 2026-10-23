package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
)

// ModelCapability is a source-scoped declaration, not execution entitlement.
// Agent catalog data must never add models to the worklite forwarding catalog.
type ModelCapability struct {
	SourceFunction    string             `json:"source_function"`
	Contexts          map[string]int64   `json:"contexts"`
	ContextOptions    map[string][]int64 `json:"context_options,omitempty"`
	MaxMode           *bool              `json:"max_mode,omitempty"`
	IsDollarMax       *bool              `json:"is_dollar_max,omitempty"`
	PromptMaxTokens   *int64             `json:"prompt_max_tokens,omitempty"`
	OutputMaxTokens   *int64             `json:"output_max_tokens,omitempty"`
	ThinkingSupported *bool              `json:"thinking_supported,omitempty"`
	ThinkingOptions   []string           `json:"thinking_options"`
	ThinkingDefault   string             `json:"thinking_default,omitempty"`
}

// ContextEvidence belongs only to the credential storing it. DeclaredContext
// remains a catalog declaration; ObservedInputTokens is the tested lower bound.
type ContextEvidence struct {
	Kind                string `json:"kind"`
	Realm               string `json:"realm"`
	Function            string `json:"function"`
	Model               string `json:"model"`
	ObservedInputTokens int64  `json:"observed_input_tokens"`
	DeclaredContext     int64  `json:"declared_context"`
	ObservedAt          string `json:"observed_at"`
	EvidenceID          string `json:"evidence_id"`
}

type catalogModel struct {
	ConfigName            string          `json:"config_name"`
	DisplayContactConfig  json.RawMessage `json:"display_contact_config"`
	NewVersion            json.RawMessage `json:"new_version"`
	Newversion            json.RawMessage `json:"newversion"`
	ConfigSwitch          *bool           `json:"config_switch"`
	IsInvisibleToUser     *bool           `json:"is_invisible_to_user"`
	ContextWindowTokens   json.RawMessage `json:"context_window_tokens"`
	ContextWindowSize     json.RawMessage `json:"context_window_size"`
	PromptMaxTokens       json.RawMessage `json:"prompt_max_tokens"`
	MaxTokens             json.RawMessage `json:"max_tokens"`
	ReasoningEffortConfig json.RawMessage `json:"reasoning_effort_config"`
	DisplayConfig         struct {
		DisplayName string `json:"display_name"`
		MaxMode     *bool  `json:"max_mode"`
		IsDollarMax *bool  `json:"is_dollar_max"`
	} `json:"display_config"`
}

func (m catalogModel) visible() bool {
	return m.ConfigName != "" && m.DisplayConfig.DisplayName != "" && (m.ConfigSwitch == nil || *m.ConfigSwitch) && (m.IsInvisibleToUser == nil || !*m.IsInvisibleToUser)
}
func positiveLimit(raw json.RawMessage) *int64 {
	n := factNumber(raw)
	if n == nil || *n <= 0 || *n > 100000000 || *n != float64(int64(*n)) {
		return nil
	}
	v := int64(*n)
	return &v
}
func contextDeclarations(raw json.RawMessage) (map[string]int64, map[string][]int64) {
	contexts := map[string]int64{}
	options := map[string][]int64{}
	for k, v := range rawObject(raw) {
		if k != "dev" && k != "max" && k != "newversion" {
			continue
		}
		if n := positiveLimit(v); n != nil {
			contexts[k] = *n
			options[k] = []int64{*n}
			continue
		}
		var list []json.RawMessage
		if json.Unmarshal(v, &list) != nil || len(list) > 16 {
			continue
		}
		seen := map[int64]bool{}
		valid := true
		for _, r := range list {
			n := positiveLimit(r)
			if n == nil {
				valid = false
				break
			}
			seen[*n] = true
		}
		if !valid {
			continue
		}
		for n := range seen {
			options[k] = append(options[k], n)
		}
		sort.Slice(options[k], func(i, j int) bool { return options[k][i] < options[k][j] })
		if len(options[k]) == 1 {
			contexts[k] = options[k][0]
		} else if k == "max" && len(options[k]) > 1 {
			contexts[k] = options[k][len(options[k])-1]
		}
	}
	return contexts, options
}
func effortLabel(raw json.RawMessage) string {
	s := factString(raw)
	if s == "" || len(s) > 32 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return ""
		}
	}
	return s
}
func modelCapability(m catalogModel, function string) ModelCapability {
	raw := m.ContextWindowTokens
	if len(raw) == 0 || string(raw) == "null" {
		raw = m.ContextWindowSize
	}
	contexts, options := contextDeclarations(raw)
	out := ModelCapability{SourceFunction: function, Contexts: contexts, ContextOptions: options, MaxMode: m.DisplayConfig.MaxMode, IsDollarMax: m.DisplayConfig.IsDollarMax, PromptMaxTokens: positiveLimit(m.PromptMaxTokens), OutputMaxTokens: positiveLimit(m.MaxTokens), ThinkingOptions: []string{}}
	d := rawObject(m.DisplayContactConfig)
	_ = json.Unmarshal(rawObject(d["reasoning"])["enable"], &out.ThinkingSupported)
	r := rawObject(m.ReasoningEffortConfig)
	if r == nil {
		r = rawObject(d["reasoning_effort_config"])
	}
	if r != nil {
		if b, ok := r["support_thinking"]; ok {
			_ = json.Unmarshal(b, &out.ThinkingSupported)
		}
		var list []json.RawMessage
		if json.Unmarshal(r["options"], &list) == nil && len(list) <= 24 {
			seen := map[string]bool{}
			for _, v := range list {
				s := effortLabel(v)
				if s == "" {
					o := rawObject(v)
					s = effortLabel(o["value"])
					if s == "" {
						s = effortLabel(o["level"])
					}
				}
				if s != "" && !seen[s] {
					out.ThinkingOptions = append(out.ThinkingOptions, s)
					seen[s] = true
				}
			}
		}
		def := effortLabel(r["default_level"])
		for _, s := range out.ThinkingOptions {
			if s == def {
				out.ThinkingDefault = def
				break
			}
		}
	}
	return out
}
func (c *Client) fetchModelCatalog(a *auth.Auth, function string) ([]catalogModel, error) {
	body := map[string]any{"function": function, "config_names": nil, "need_prompt": false, "current_config_info": nil, "poly_prompt": true, "mode_type": nil, "agent_type": nil}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.agentBase()+EpModels, bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	SOLOHeaders(req, a, false)
	data, e := c.doJSON(req)
	if e != nil {
		return nil, e
	}
	var response struct {
		Models []catalogModel `json:"config_info_list"`
	}
	if e = json.Unmarshal(data, &response); e != nil {
		return nil, fmt.Errorf("models parse failed")
	}
	if len(response.Models) > 4096 {
		return nil, fmt.Errorf("model catalog exceeds limit")
	}
	return response.Models, nil
}
func (c *Client) enrichCapabilities(a *auth.Auth, models []ModelInfo) {
	if a.Variant != "" && !strings.EqualFold(a.Variant, "cn") && !strings.EqualFold(a.Variant, "solo") {
		return
	}
	rows, e := c.fetchModelCatalog(a, "solo_agent")
	if e != nil {
		for i := range models {
			models[i].Facts.CapabilityStatus = "partial"
		}
		return
	}
	byID := map[string]catalogModel{}
	duplicates := map[string]bool{}
	for _, r := range rows {
		if !r.visible() {
			continue
		}
		if _, ok := byID[r.ConfigName]; ok {
			duplicates[r.ConfigName] = true
		}
		byID[r.ConfigName] = r
	}
	for i := range models {
		r, ok := byID[models[i].ID]
		if !ok || duplicates[models[i].ID] {
			continue
		}
		models[i].Facts.CapabilityCatalog = append(models[i].Facts.CapabilityCatalog, modelCapability(r, "solo_agent"))
	}
}
