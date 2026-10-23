// Read-only management candidate union; never used as account authorization.
package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// modelGroupVariantOrder is the fixed presentation order; a variant with
// zero credentials still renders (empty group) so the picker shows every
// channel.
var modelGroupVariantOrder = []string{"cn", "solo", "intl"}

// modelGroupVariantLabel renders the human-facing variant title. Keep in sync
// with the login_variant enum description in main.go and the panel badges.
func modelGroupVariantLabel(variant string) string {
	switch variant {
	case "cn":
		return "CN（trae.cn）"
	case "solo":
		return "Solo CN（trae.cn SOLO）"
	case "intl":
		return "Intl（trae.ai）"
	default:
		return variant
	}
}

// modelGroupEntry is one catalog row. Name carries the display name when the
// snapshot/discovery provided one.
type modelGroupEntry struct {
	ID                 string              `json:"id"`
	BaseID             string              `json:"base_id"`
	Name               string              `json:"name,omitempty"`
	ContextLength      int64               `json:"context_length,omitempty"`
	ContextLengths     []int64             `json:"context_lengths,omitempty"`
	Credentials        int                 `json:"credentials"`
	Multiplier         *float64            `json:"multiplier"`
	Thinking           any                 `json:"thinking"`
	ThinkingState      string              `json:"thinking_state"`
	Context            *int64              `json:"context"`
	ContextState       string              `json:"context_state"`
	MultiplierState    string              `json:"multiplier_state"`
	Multipliers        []float64           `json:"multipliers"`
	Facts              []accountModelFacts `json:"facts"`
	UnknownCredentials int                 `json:"unknown_credentials"`
}

// modelGroup is one variant's catalog as the panel picker consumes it.
type modelGroup struct {
	Realm        string            `json:"realm"`
	Label        string            `json:"label"`
	Models       []modelGroupEntry `json:"models"`
	Count        int               `json:"count"`
	Source       string            `json:"source,omitempty"`     // snapshot | refreshed
	FetchedAt    string            `json:"fetched_at,omitempty"` // RFC3339 UTC
	NeedsRefresh bool              `json:"needs_refresh"`        // empty catalog → suggest the refresh button
	Credentials  int               `json:"credentials"`          // trae credentials seen in this variant
	Note         string            `json:"note,omitempty"`       // refresh failure reason when a refresh produced nothing
}

type modelGroupsResponse struct {
	Groups        []modelGroup            `json:"groups"`
	Order         []string                `json:"model_order"`
	CandidateOnly bool                    `json:"candidate_only"`
	Hint          string                  `json:"hint"`
	Promotions    modelPromotionsResponse `json:"promotions"`
}

// variantCatalogEntry is one credential's contribution to a variant catalog scan.
type variantCatalogEntry struct {
	variant  string
	hasToken bool
	disabled bool
	name     string // host file name — drives the refresh persist path
	storage  []byte // physical credential document (read-only view)
	snapshot *persistedModelCache
}

// handleModelGroupsQuery answers GET /plugins/trae/models/groups.
// Returns (status, payload); status 200 carries modelGroupsResponse.
func handleModelGroupsQuery(req pluginapi.ManagementRequest) (int, any) {
	refresh := strings.TrimSpace(req.Query.Get("refresh")) == "1"

	files, err := hostAuthListFn()
	if err != nil {
		return http.StatusInternalServerError, map[string]any{"error": "auth list failed: " + err.Error()}
	}

	perVariant := map[string][]*variantCatalogEntry{}
	for _, f := range files {
		phys, err := hostAuthRawFn(f.AuthIndex)
		if err != nil || len(phys) == 0 {
			continue
		}
		// hostAuthListFn already filtered foreign owners; variant resolution
		// mirrors persistedSnapshotForStorage's same-variant guard.
		variant := sniffVariantFromJSON(phys)
		var flags struct {
			Disabled bool `json:"disabled"`
		}
		_ = json.Unmarshal(phys, &flags)
		e := &variantCatalogEntry{
			variant:  variant,
			disabled: f.Disabled || flags.Disabled,
			hasToken: strings.TrimSpace(extractTraeAccessToken(phys)) != "",
			name:     f.Name,
			storage:  phys,
		}
		if c := readModelCacheDoc(phys); c != nil && c.Realm == variant {
			e.snapshot = c
		}
		perVariant[variant] = append(perVariant[variant], e)
	}

	groups := make([]modelGroup, 0, len(modelGroupVariantOrder))
	for _, variant := range modelGroupVariantOrder {
		groups = append(groups, buildModelGroup(variant, perVariant[variant], refresh))
	}
	return http.StatusOK, modelGroupsResponse{
		Groups:     groups,
		Promotions: promotionsForGroups(groups, time.Now()),
		Order:      currentModelOrder(), CandidateOnly: true,
		Hint: "勾选=启用；取消勾选保存后该模型从渠道注册列表移除（ID 带渠道后缀）。与 CPA 全局页驱动同一份 oauth-excluded-models 配置。",
	}
}

// buildModelGroup unions every same-variant account catalog for management.
// Refresh is per credential; no account is treated as representative for peers.
func buildModelGroup(variant string, entries []*variantCatalogEntry, refresh bool) modelGroup {
	g := modelGroup{Realm: variant, Label: modelGroupVariantLabel(variant), Models: []modelGroupEntry{}, Credentials: len(entries)}
	// Every credential contributes its own catalog. A newer credential is not a
	// representative for the region; aggregation here is candidate-only.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	rows := map[string]*modelGroupEntry{}
	ids := []string{}
	for _, e := range entries {
		var models []pluginapi.ModelInfo
		if e.snapshot != nil {
			models = e.snapshot.Models
			if g.Source != "queried" {
				g.Source = "snapshot"
			}
			if e.snapshot.FetchedAt > g.FetchedAt {
				g.FetchedAt = e.snapshot.FetchedAt
			}
		}
		if refresh && e.hasToken && !e.disabled {
			if fresh := refreshVariantCatalog(e); len(fresh) > 0 {
				models = fresh
				// A refresh can legitimately return last-good; do not relabel it fresh.
				g.Source = "queried"
				reloadCatalogSnapshot(e)
				if e.snapshot != nil && e.snapshot.FetchedAt > g.FetchedAt {
					g.FetchedAt = e.snapshot.FetchedAt
				}
			}
		}
		seen := map[string]bool{}
		for _, m := range modelGroupEntries(models) {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			row := rows[m.ID]
			if row == nil {
				copy := m
				row = &copy
				row.BaseID = catalogBaseID(m.ID, variant)
				rows[m.ID] = row
				ids = append(ids, m.ID)
			}
			row.Credentials++
			if m.ContextLength > 0 {
				found := false
				for _, n := range row.ContextLengths {
					if n == m.ContextLength {
						found = true
					}
				}
				if !found {
					row.ContextLengths = append(row.ContextLengths, m.ContextLength)
				}
			}
		}
	}
	sort.Strings(ids)
	ordered := make([]pluginapi.ModelInfo, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, pluginapi.ModelInfo{ID: id})
	}
	for _, m := range applyModelOrder(ordered) {
		row := rows[m.ID]
		sort.Slice(row.ContextLengths, func(i, j int) bool { return row.ContextLengths[i] < row.ContextLengths[j] })
		if len(row.ContextLengths) != 1 {
			row.ContextLength = 0
		} else {
			row.ContextLength = row.ContextLengths[0]
		}
		g.Models = append(g.Models, *row)
	}
	enrichModelGroup(&g, entries, time.Now())
	g.Count = len(g.Models)
	g.NeedsRefresh = g.Count == 0
	if refresh {
		g.Note = "已逐凭证查询（跳过禁用账号）；失败时仅保留各自最近目录，候选汇总不代表每个账号均有权限"
	}
	if g.Count == 0 {
		g.Note = "尚无已验证目录；可刷新目录，不使用猜测的模型或其他账号权限"
	}
	return g
}

// refreshVariantCatalog runs ONE live discovery for the picked credential
// through the exact path model.for_auth uses (intl credentials ride the
// intl chain, cn/solo the solo_work_lite lane) — pre-exclusion, so the
// picker can also un-exclude.
func refreshVariantCatalog(pick *variantCatalogEntry) []pluginapi.ModelInfo {
	if pick == nil || len(pick.storage) == 0 {
		return nil
	}
	if pick.variant == variantIntl {
		a, err := intlparseStoredAuth(pick.storage)
		if err != nil {
			return nil
		}
		return intlmodelCatalogForAuth(pick.storage, pick.name, a)
	}
	a, err := parseStoredAuth(pick.storage)
	if err != nil {
		return nil
	}
	return modelsForVariant(a, pick.storage)
}

// modelGroupEntries projects ModelInfos into picker rows (id + display name).
func modelGroupEntries(models []pluginapi.ModelInfo) []modelGroupEntry {
	out := make([]modelGroupEntry, 0, len(models))
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.Name
		}
		if strings.EqualFold(name, m.ID) {
			name = ""
		}
		out = append(out, modelGroupEntry{ID: m.ID, Name: name, ContextLength: m.ContextLength})
	}
	return out
}
