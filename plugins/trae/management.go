// management.go implements the Trae SOLO CN management API and web panel:
// the account dashboard (uid, nickname, credits, plan, check-in status),
// manual check-in (single or all accounts), credit query, token refresh,
// and account observations and host-owned disabled state.
//
// Routes are exposed under two prefixes:
//   - /v0/management/plugins/trae-solo-cn/*  — JSON endpoints (CPA-management-
//     authenticated by host middleware; plugin-layer auth only kicks in when
//     management_key is configured).
//   - /v0/resource/plugins/trae-solo-cn/*    — unauthenticated browser UI
//     (panel.html) and menu entries surfaced in the CPA management UI.
//
// The contract follows workbuddy/management.go: handleManagement returns an
// envelope-wrapped pluginapi.ManagementResponse (envelope is consumed by
// CPA's rpcPluginAdapter.callPlugin[pluginapi.ManagementResponse]; CPA's
// HTTP layer writes only resp.Body to the browser, so JSON.parse / HTML
// rendering work transparently).
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// -----------------------------------------------------------------------------
// Route + response types
// -----------------------------------------------------------------------------

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

// managementBasePathCache holds the host-injected BasePath so handleManagement
// doesn't hardcode /v0/management. Falls back to the historical default if the
// host doesn't provide one (older CPA builds).
var (
	managementBasePathCache   = "/v0/management"
	managementBasePathCacheMu sync.RWMutex
)

func loadedManagementBasePath() string {
	managementBasePathCacheMu.RLock()
	defer managementBasePathCacheMu.RUnlock()
	return managementBasePathCache
}

func setManagementBasePath(p string) {
	p = strings.TrimRight(strings.TrimSpace(p), "/")
	if p == "" {
		return
	}
	managementBasePathCacheMu.Lock()
	managementBasePathCache = p
	managementBasePathCacheMu.Unlock()
}

// managementRegistration describes the routes + resources this plugin serves.
// Paths are registered relative to /plugins/trae-solo-cn — the host prepends
// either /v0/management or /v0/resource/plugins/<provider> based on which list
// they appear in (Routes vs Resources).
func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + providerName
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodPost, Path: base + "/login/start", Description: "Start a protected CN/SOLO/Intl login task."},
			{Method: http.MethodPost, Path: base + "/login/poll", Description: "Resume a login task and safely save its result."},
			{Method: http.MethodGet, Path: base + "/login/status", Description: "Read a login task status without exposing credentials."},
			{Method: http.MethodPost, Path: base + "/accounts/action", Description: "Rename or explicitly enable/disable one account."},
			{Method: http.MethodPost, Path: base + "/accounts/refresh", Description: "Refresh and persist one existing account."},
			{Method: http.MethodGet, Path: base + "/accounts", Description: "List Trae SOLO CN accounts with credits, plan, and check-in status."},
			{Method: http.MethodPost, Path: base + "/checkin", Description: "Manually check in one account (auth_index) or all accounts."},
			{Method: http.MethodGet, Path: base + "/credits", Description: "Get real-time credits for one (auth_index query) or all accounts."},
			{Method: http.MethodPost, Path: base + "/refresh", Description: "Force refresh access tokens for all accounts and return the refreshed dashboard (accounts)."},
			{Method: http.MethodGet, Path: base + "/status", Description: "Account facts: scoped quota observations and host disabled state."},
			{Method: http.MethodPost, Path: base + "/release", Description: "Release cached upstream quota observations for one auth_index; does not enable disabled accounts."},
			{Method: http.MethodPost, Path: base + "/import", Description: "Import Trae credential JSON (nested or flat) into host auth store."},
			{Method: http.MethodGet, Path: base + "/intl/accounts", Description: "Trae Intl: list accounts with uid, nickname, and token expiry."},
			{Method: http.MethodGet, Path: base + "/intl/status", Description: "Trae Intl: plugin status."},
			{Method: http.MethodPost, Path: base + "/intl/import", Description: "Trae Intl: import credential JSON into host auth store."},
			{Method: http.MethodGet, Path: base + "/models/groups", Description: "Per-variant model catalog (model_cache snapshots; ?refresh=1 re-discovers) for the panel's exclusion picker."},
		},
		// Single menu entry (v0.12.2): /panel covers CN + SOLO + Intl
		// accounts. Legacy /intl_panel path serves the same panel.
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "Trae", Description: "Trae dashboard: CN/SOLO credits, check-in, accounts + Intl accounts."},
			// Menu-less: routable browser resources without UI entries.
			{Path: "/intl_panel", Description: "Trae Intl dashboard (linked from the main panel)."},
			{Path: "/oauth_callback", Description: "OAuth login callback redirect target."},
			{Path: "/oauth_submit", Description: "Paste-to-complete fallback: GET with cb_url=<url-encoded failed redirect URL> finishes the login remotely (host resource routes are GET-only)."},
			{Path: "/login_status", Description: "Live login state for the panel status line: pending variant, callback endpoint, TTL remaining, last outcome (data only, no secrets)."},
		},
	}
}

// -----------------------------------------------------------------------------
// handleManagement
// -----------------------------------------------------------------------------

// handleManagement dispatches one ManagementRequest to the right handler and
// returns an envelope-wrapped pluginapi.ManagementResponse.
//
// Envelope shape contract: CPA's rpcPluginAdapter.callPlugin decodes the
// envelope, extracts Result, and unmarshals it into pluginapi.ManagementResponse.
// CPA's HTTP layer (ServeManagementHTTP / ServeResourceHTTP) writes only
// resp.Body to the browser (with resp.Headers / resp.StatusCode) — the envelope
// never reaches the browser, so JSON.parse works on the raw JSON body.
func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimRight(req.Path, "/")

	// Browser UI resource routes (unauthenticated).
	resPrefix := "/v0/resource/plugins/" + providerName
	if req.Method == http.MethodGet && path == resPrefix+"/oauth_callback" {
		return okEnvelope(mgmtHTMLResponse(handleOAuthCallbackResource(req)))
	}
	if (req.Method == http.MethodGet || req.Method == http.MethodPost) && path == resPrefix+"/oauth_submit" {
		return okEnvelope(mgmtHTMLResponse(handleOAuthSubmitResource(req)))
	}
	if req.Method == http.MethodGet && path == resPrefix+"/login_status" {
		return okEnvelope(mgmtJSONResourceResponse(handleLoginStatusResource()))
	}
	if req.Method == http.MethodGet && strings.HasPrefix(path, resPrefix) {
		sub := strings.TrimPrefix(path, resPrefix)
		return okEnvelope(mgmtHTMLResponse(servePanel(sub)))
	}

	base := loadedManagementBasePath() + "/plugins/" + providerName
	switch {
	case req.Method == http.MethodPost && path == base+"/login/start":
		return okEnvelope(handleOAuthManagementStart(req))
	case req.Method == http.MethodPost && path == base+"/login/poll":
		return okEnvelope(handleOAuthManagementPoll(req))
	case req.Method == http.MethodGet && path == base+"/login/status":
		return okEnvelope(handleOAuthManagementStatus(req))
	case req.Method == http.MethodPost && path == base+"/accounts/action":
		status, payload := handleAccountActionQuery(req)
		return okEnvelope(mgmtJSONResponse(status, payload))
	case req.Method == http.MethodPost && path == base+"/accounts/refresh":
		var body struct {
			AuthIndex string `json:"auth_index"`
		}
		if json.Unmarshal(req.Body, &body) != nil || strings.TrimSpace(body.AuthIndex) == "" {
			return okEnvelope(mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "auth_index is required"}))
		}
		return okEnvelope(mgmtJSONResponse(http.StatusOK, refreshAccount(strings.TrimSpace(body.AuthIndex), true)))
	case req.Method == http.MethodGet && path == base+"/accounts":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, buildDashboard()))
	case req.Method == http.MethodPost && path == base+"/checkin":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleManualCheckin(req)))
	case req.Method == http.MethodGet && path == base+"/credits":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCreditsQuery(req)))
	case req.Method == http.MethodPost && path == base+"/refresh":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleRefresh()))
	case req.Method == http.MethodGet && path == base+"/status":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, buildMaintenanceStatus()))
	case req.Method == http.MethodPost && path == base+"/release":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleCooldownRelease(req)))
	case req.Method == http.MethodPost && path == base+"/import":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, handleImportAuth(req)))
	case req.Method == http.MethodGet && path == base+"/models/groups":
		status, payload := handleModelGroupsQuery(req)
		return okEnvelope(mgmtJSONResponse(status, payload))
	}
	return okEnvelope(mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": "not found: " + path}))
}

// -----------------------------------------------------------------------------
// Response helpers
// -----------------------------------------------------------------------------

func mgmtJSONResponse(status int, v any) pluginapi.ManagementResponse {
	body, _ := json.Marshal(v)
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	return pluginapi.ManagementResponse{StatusCode: status, Headers: h, Body: body}
}

func mgmtHTMLResponse(body []byte) pluginapi.ManagementResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: body}
}

// -----------------------------------------------------------------------------
// Panel HTML embed
// -----------------------------------------------------------------------------

//go:embed panel.html
var panelHTML []byte

// servePanel returns the embedded panel.html for valid sub-paths, or a 404
// stub for unknown resources.
func servePanel(sub string) []byte {
	// /intl_panel stays as a hidden alias — it serves the unified panel
	// (v0.12.2 removed the separate Intl menu entry).
	if sub != "" && sub != "/" && sub != "/panel" && sub != "/panel.html" && sub != "/intl_panel" {
		return []byte("<h1>404</h1>")
	}
	return panelHTML
}

// -----------------------------------------------------------------------------
// Dashboard / account listing
// -----------------------------------------------------------------------------

// traeAccount is one row of the dashboard. It is built from the host auth
// store (host.auth.list) so it reflects the CPA UI's on-disk truth — the
// Physical snapshots prevent stale registrations from reviving deleted files.
type traeAccount struct {
	AuthIndex string       `json:"auth_index"`
	AuthID    string       `json:"auth_id,omitempty"`
	Name      string       `json:"name"`
	Label     string       `json:"label"`
	UID       string       `json:"uid"`
	Nickname  string       `json:"nickname"`
	Status    string       `json:"status"`
	Disabled  bool         `json:"disabled"`
	Credits   *traeCredits `json:"credits,omitempty"`
	Checkin   *traeCheckin `json:"checkin,omitempty"`
	Error     string       `json:"error,omitempty"`
	Variant   string       `json:"variant,omitempty"`
	// v0.12.32: 凭证文件是否携带 deviceId。官方 claim 要求 x-device-id 携带
	// 真实绑定 did，缺失时服务端可能静默不入账 —— 面板徽标告警用。
	DeviceIDSet bool `json:"device_id_set"`
	// v0.12.44: 凭证谱系 + CheckLogin 探测的服务端绑定设备状态。
	Platform         string `json:"platform,omitempty"`           // platformId（trae_solo_cn 等）
	BoundDeviceID    string `json:"bound_device_id,omitempty"`    // 服务端绑定的 deviceId
	DeviceBindStatus string `json:"device_bind_status,omitempty"` // BOUND / 其他
	DeviceMatch      *bool  `json:"device_match,omitempty"`       // nil = 未探测
	IsLogin          *bool  `json:"is_login,omitempty"`           // nil = 未探测
}

type traeCredits struct {
	// TotalRemain is measured subscription quota. nil means unknown, not zero.
	// Check-in credits are the daily reward amount, never a wallet balance.
	TotalRemain *int64 `json:"total_remain"`
	Plan        string `json:"plan"`
	FetchedAt   string `json:"fetched_at,omitempty"`
	// v0.12.28 用量模型扩展（对齐 trae.ts TraeUsage）。
	UsageModel  string `json:"usage_model,omitempty"` // fast|basic|unknown
	RemainKnown bool   `json:"remain_known"`
	Used        *int64 `json:"used,omitempty"`  // basic: 已用
	Total       *int64 `json:"total,omitempty"` // basic: 额度池
	FastLimit   *int64 `json:"fast_limit,omitempty"`
	FastUsed    *int64 `json:"fast_used,omitempty"`
	// v0.12.29 ide_user_pay_status 补充维度（Free/SOLO 账户的真实数值）。
	FastRequestPer *int64 `json:"fast_request_per,omitempty"` // 快请求/月
	SoloParallel   *int64 `json:"solo_parallel,omitempty"`    // SOLO 并发数
	SoloPackage    bool   `json:"solo_package,omitempty"`     // enable_solo_* 任一
	PlanType       string `json:"plan_type,omitempty"`        // user_pay_identity_str

	// v0.12.34: 官方 cashier 同口径积分池（模型调用真正扣减的钱；
	// 签到奖励数额仅用于签到展示，不能当作余额）。
	CreditsPoolRemain    *int64 `json:"credits_pool_remain,omitempty"`
	CreditsPoolKnown     bool   `json:"credits_pool_known,omitempty"`
	CreditsPoolUnlimited bool   `json:"credits_pool_unlimited,omitempty"`
	CreditsPoolTotal     *int64 `json:"credits_pool_total,omitempty"`
	CreditsPoolUsed      *int64 `json:"credits_pool_used,omitempty"`
	CreditsPoolPackCount int    `json:"credits_pool_pack_count,omitempty"`
}

type traeCheckin struct {
	CheckedIn bool  `json:"checked_in"`
	Credits   int64 `json:"credits"`
	Enable    bool  `json:"enable"`
}

// buildDashboard aggregates every Trae SOLO CN credential from the host auth
// store, plus a snapshot of credits / checkin status from the account cache
// (read-only; live probes are explicit management operations).
func buildDashboard() map[string]any {
	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	out := make([]traeAccount, 0, len(files))
	for _, f := range files {
		acct := traeAccount{
			AuthIndex: f.AuthIndex,
			AuthID:    f.ID,
			Name:      f.Name,
			Label:     f.Label,
			Status:    f.Status,
			Disabled:  f.Disabled,
		}
		snapshot, err := freshAuthSnapshot(f.AuthIndex)
		if err != nil {
			acct.Error = "load auth: " + err.Error()
			out = append(out, acct)
			continue
		}
		sa := snapshot.Stored
		acct.Disabled = snapshot.File.Disabled
		acct.UID = sa.Account.UID
		acct.Nickname = sa.Account.Nickname
		acct.Variant = sa.Variant
		acct.DeviceIDSet = strings.TrimSpace(snapshot.Auth.DeviceID) != ""
		// v0.12.44: 凭证自证谱系 + 缓存的 CheckLogin 绑定快照。
		acct.Platform = upstream.PlatformIDFor(sa.Variant)
		if v, ok := accountCache.Load(f.AuthIndex); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 && e.bind != nil && e.bind.Known {
				acct.BoundDeviceID = e.bind.BoundDeviceID
				acct.DeviceBindStatus = e.bind.DeviceBindStatus
				dm := e.bind.DeviceMatch
				il := e.bind.IsLogin
				acct.DeviceMatch = &dm
				acct.IsLogin = &il
			}
		}

		// Cached credits / checkin (filled by scheduler + manual endpoints).
		// v0.12.25: credits < 0 = pack quota never fetched — leave the
		// object off so the panel lazy-loads it via /credits.
		if v, ok := accountCache.Load(f.AuthIndex); ok {
			if e, ok2 := v.(*accountCacheEntry); ok2 && (e.credits >= 0 || e.usageFilled) {
				acct.Credits = creditsFromCache(e)
			}
			if e, ok2 := v.(*accountCacheEntry); ok2 && e.checkin != nil && e.checkinObserved.In(checkinDayLocation).Format("2006-01-02") == time.Now().In(checkinDayLocation).Format("2006-01-02") {
				acct.Checkin = &traeCheckin{
					CheckedIn: e.checkin.CheckedIn,
					Credits:   e.checkin.Credits,
					Enable:    e.checkin.Enable,
				}
			}
		}
		out = append(out, acct)
	}
	return map[string]any{
		"accounts":    out,
		"provider":    providerName,
		"server_time": time.Now().Format("2006-01-02 15:04:05"),
	}
}

func handleCooldownRelease(req pluginapi.ManagementRequest) map[string]any {
	return releaseAccountObservation(req)
}

// -----------------------------------------------------------------------------
// host auth bridge (host.auth.list / host.auth.get)
// -----------------------------------------------------------------------------

// rpcHostAuthListResponse mirrors the host's host.auth.list envelope result.
type rpcHostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type rpcHostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	JSON      json.RawMessage `json:"json"`
}

// hostAuthList returns all Trae SOLO CN credentials known to the host. We
// filter by filename prefix because some legacy auth files don't carry a
// "type"/"provider" field (pre-config-convention files).
func hostAuthList() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := hostCall(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.list: bad envelope")
	}
	var resp rpcHostAuthListResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	out := make([]pluginapi.HostAuthFileEntry, 0, len(resp.Files))
	prefix := providerName + "-"
	for _, f := range resp.Files {
		if strings.HasPrefix(strings.ToLower(f.Name), prefix) {
			out = append(out, f)
		}
	}
	return out, nil
}

// hostAuthGet fetches one credential (by auth_index) from the host and parses
// it into the nested {auth, account} shape used by upstreamClient.
type storedAuth struct {
	Auth    storedTokens  `json:"auth"`
	Account storedAccount `json:"account"`
	// Variant is not persisted separately (it lives in Auth in the JSON);
	// it is resolved at load time (hostAuthGet) so management operations
	// hit the right upstream endpoints for cn/solo/intl accounts (v0.12.2).
	Variant string `json:"-"`
}

type storedTokens struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
	Domain       string `json:"domain"`
	APIHost      string `json:"apiHost"`
	MachineID    string `json:"machineId"`
	DeviceID     string `json:"deviceId"`
	Variant      string `json:"variant"`
	// Region is intl-only (auth.region, e.g. "US-East") — feeds X-User-Region
	// on the intl pay face (intl_pay.go).
	Region string `json:"region"`
	// BoundDeviceID mirrors the credential-parity extra auth.boundDeviceId
	// (issue #29 field report): intl logins before v0.12.72 persisted the
	// OAuth-bound device id ONLY under this key — auth.deviceId was absent,
	// so device_id_set surfaced false. Read as the DeviceID fallback.
	BoundDeviceID string `json:"boundDeviceId"`
}

type storedAccount struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}

func hostAuthGet(authIndex string) (*storedAuth, error) {
	reqBody, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	raw, err := hostCall(pluginabi.MethodHostAuthGet, reqBody)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		return nil, fmt.Errorf("host.auth.get: bad envelope")
	}
	var resp rpcHostAuthGetResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return nil, err
	}
	var sa storedAuth
	if err := json.Unmarshal(resp.JSON, &sa); err != nil {
		return nil, fmt.Errorf("parse stored auth: %w", err)
	}
	// Explicit auth.variant wins; sniff covers legacy files (v0.12.2).
	sa.Variant = sa.Auth.Variant
	if sa.Variant == "" {
		sa.Variant = sniffVariantFromJSON(resp.JSON)
	}
	return &sa, nil
}

// hostAuthAsUpstream converts the host-stored nested shape into the upstream
// *auth.Auth the trae-solo-cn upstream client expects.
func hostAuthAsUpstream(sa *storedAuth) *auth.Auth {
	deviceID := sa.Auth.DeviceID
	if deviceID == "" {
		// v0.12.72 (issue #29 field report): legacy intl files carry the bound
		// device id only under auth.boundDeviceId — fall back so device_id_set
		// reflects reality and X-Device-Id headers carry the bound id.
		deviceID = sa.Auth.BoundDeviceID
	}
	return &auth.Auth{
		AccessToken:  sa.Auth.AccessToken,
		RefreshToken: sa.Auth.RefreshToken,
		ExpiresAt:    sa.Auth.ExpiresAt,
		Domain:       sa.Auth.Domain,
		APIHost:      sa.Auth.APIHost,
		MachineID:    sa.Auth.MachineID,
		DeviceID:     deviceID,
		UID:          sa.Account.UID,
		EnterpriseID: sa.Account.EnterpriseID,
		Nickname:     sa.Account.Nickname,
		Variant:      sa.Variant,
	}
}

// isIntlStoredAuth reports whether the account lives on the intl realm and
// therefore must use the intl faces (chat via intlupstream, billing via
// intl_pay.go's grow-normal.trae.ai + v1) instead of the CN ones.
func isIntlStoredAuth(sa *storedAuth) bool {
	if sa == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(sa.Variant), "intl") {
		return true
	}
	d := strings.ToLower(strings.TrimSpace(sa.Auth.Domain))
	return d == "trae.ai" || d == "marscode.com" || strings.HasSuffix(d, ".trae.ai")
}

// -----------------------------------------------------------------------------
// Manual management endpoints
// -----------------------------------------------------------------------------

// creditsFromCache projects the cache entry onto the dashboard's traeCredits.
// v0.12.28: usageFilled entries carry the upstream usage model; legacy
// cache entries (pre-upgrade) keep the old pack-only number.
func creditsFromCache(e *accountCacheEntry) *traeCredits {
	if e.usageFilled {
		c := &traeCredits{
			Plan:        e.usagePlan(),
			FetchedAt:   e.fetched.Format(time.RFC3339),
			UsageModel:  e.usage.UsageModel,
			RemainKnown: e.usage.RemainKnown,
		}
		if e.usage.RemainKnown {
			r := e.usage.Remain
			c.TotalRemain = &r
		}
		if e.usage.UsageModel == "basic" {
			u, t := e.usage.Used, e.usage.Total
			c.Used, c.Total = &u, &t
		}
		if e.usage.UsageModel == "fast" {
			fl, fu := e.usage.FastLimit, e.usage.FastUsed
			c.FastLimit, c.FastUsed = &fl, &fu
		}
		// v0.12.29: pay_status 补充维度随缓存透出（/accounts 免刷新可见）。
		c.FastRequestPer = e.usage.FastRequestPer
		c.SoloParallel = e.usage.SoloParallel
		c.SoloPackage = e.usage.SoloPackage
		c.PlanType = e.usage.PlanType
		// v0.12.34: 积分池随缓存透出（/accounts 免刷新可见）。
		if e.usage.CreditsPool.Known {
			pr := e.usage.CreditsPool.Remain
			c.CreditsPoolRemain = &pr
			c.CreditsPoolKnown = true
			c.CreditsPoolUnlimited = e.usage.CreditsPool.Unlimited
			if e.usage.CreditsPool.BreakdownKnown && !e.usage.CreditsPool.Unlimited {
				total, used := e.usage.CreditsPool.Total, e.usage.CreditsPool.Used
				c.CreditsPoolTotal, c.CreditsPoolUsed = &total, &used
				c.CreditsPoolPackCount = e.usage.CreditsPool.PackCount
			}
		}
		return c
	}
	if e.credits < 0 {
		return nil
	}
	remain := e.credits
	return &traeCredits{TotalRemain: &remain, FetchedAt: e.fetched.Format(time.RFC3339), RemainKnown: true, UsageModel: "legacy"}
}

// usagePlan returns the plan label stored alongside the usage snapshot
// (empty until handleCreditsQuery fills it).
func (e *accountCacheEntry) usagePlan() string { return e.plan }

// Manual, automatic and retry paths all enter checkinAccount's account lock.
func handleManualCheckin(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
		UID       string `json:"uid"`
	}
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return map[string]any{"error": "invalid JSON"}
		}
	}
	files, err := maintenanceAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	indices := []string{}
	for _, f := range files {
		if body.AuthIndex != "" {
			if f.AuthIndex == body.AuthIndex {
				indices = append(indices, f.AuthIndex)
			}
			continue
		}
		if body.UID != "" {
			s, err := freshAuthSnapshot(f.AuthIndex)
			if err != nil || s.Auth.UID != body.UID {
				continue
			}
		}
		indices = append(indices, f.AuthIndex)
	}
	if body.AuthIndex != "" && len(indices) == 0 {
		return map[string]any{"error": "stale_index: refresh account inventory", "stale_index": true, "results": []any{}}
	}
	results := []map[string]any{}
	for _, index := range indices {
		results = append(results, checkinAccount(index))
	}
	return map[string]any{"provider": providerName, "results": results, "server_time": time.Now().Format(time.RFC3339)}
}

// checkinCacheEntry rebuilds the accountCache entry after a manual checkin.
//
// v0.12.61 ("签到成功但积分不变" report 2026-09-26): a claim that upstream
// accepted (code:0 — a real grant or the documented idempotent echo) pays out
// asynchronously, so carrying the PRE-checkin usage/pool snapshot forward
// froze the 积分余额 display on the old number until the next scheduler
// cycle. When claimAccepted, the returned entry carries NO funds snapshot
// (credits=-1, usageFilled=false) — buildDashboard omits the credits row and
// the panel lazy-loads /credits, a live fetch, instead. A failed claim (9074
// etc.) changed nothing upstream: carry the previous snapshot unchanged.
// The checkin card itself (award amount / checked_in flag) always refreshes
// from the fresh status.
func checkinCacheEntry(prev *accountCacheEntry, claimAccepted bool, status *upstream.CheckinStatusResult) *accountCacheEntry {
	e := &accountCacheEntry{credits: -1}
	if prev != nil {
		*e = *prev
	}
	e.checkin = &checkinStatus{CheckedIn: status.CheckedIn || status.DidCheckedIn, Credits: status.Credits, Enable: status.Enable}
	e.checkinObserved = time.Now()
	if claimAccepted {
		e.credits = -1
		e.usageFilled = false
		e.usageObserved = e.checkinObserved
	}

	return e
}

// cacheUsage/cachePlan read the current usage snapshot without mutating cache.
func cacheUsage(authIndex string) (u upstream.UsageSummary) {
	if v, ok := accountCache.Load(authIndex); ok {
		if e, ok2 := v.(*accountCacheEntry); ok2 {
			return e.usage
		}
	}
	return
}
func cachePlan(authIndex string) (p string) {
	if v, ok := accountCache.Load(authIndex); ok {
		if e, ok2 := v.(*accountCacheEntry); ok2 {
			return e.plan
		}
	}
	return
}

// quotaExhaustedKnown reports whether every KNOWN quota source in the summary
// is zero AND at least one source is known — a trusted "credit exhausted".
// Since 0.12.65 unknown sources never stamp 0, so a parsed 0 is the upstream's
// real answer; anything unknown here simply does not vote (v0.12.66).
func quotaExhaustedKnown(sum upstream.UsageSummary) bool {
	known := false
	if sum.RemainKnown {
		if sum.Remain != 0 {
			return false
		}
		known = true
	}
	if sum.CreditsPool.Known {
		if sum.CreditsPool.Remain != 0 {
			return false
		}
		known = true
	}
	return known
}

// handleCreditsQuery fetches live credits from upstream for one (auth_index)
// or all accounts. Updates the cache so the next /accounts reflects the new
// numbers.
// v0.12.70: per-account body extracted into refreshAccountCredits —
// handleRefresh (top-bar 刷新) runs the same live path so the returned
// dashboard carries fresh numbers instead of a stale cache projection.
func handleCreditsQuery(req pluginapi.ManagementRequest) map[string]any {
	// Read auth_index from query string (?auth_index=xxx) first, then body JSON.
	// panel.html uses GET /credits?auth_index=xxx, so req.Query is the primary source.
	authIndex := ""
	if req.Query != nil {
		authIndex = strings.TrimSpace(req.Query.Get("auth_index"))
	}
	if authIndex == "" && len(req.Body) > 0 {
		var body struct {
			AuthIndex string `json:"auth_index"`
		}
		_ = json.Unmarshal(req.Body, &body)
		authIndex = body.AuthIndex
	}
	results := []map[string]any{}

	files, err := hostAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	for _, f := range files {
		if authIndex != "" && f.AuthIndex != authIndex {
			continue
		}
		results = append(results, refreshAccount(f.AuthIndex, false))
	}
	return map[string]any{
		"provider":    providerName,
		"results":     results,
		"server_time": time.Now().Format("2006-01-02 15:04:05"),
	}
}

// refreshAccountCredits performs the live per-account credits query
// (ent_usage + credits pool + pay_status + CheckLogin + checkin status),
// stores independent factual cache fields and returns the
// result entry for the caller's results array.
//
// v0.12.70: extracted from handleCreditsQuery so handleRefresh can run the
// exact same live path. The top-bar 刷新 used to swap tokens only and then
// render buildDashboard() from the stale accountCache snapshot — the panel
// toast said 数据已刷新 while the credit bar kept showing old numbers
// (user-reported as 刷新显示有 bug). Callers pass the already-loaded
// storedAuth/upstream auth; token-refresh failures are the caller's
// decision to skip (same dead token would just fail upstream again).
func refreshAccountCredits(f pluginapi.HostAuthFileEntry, sa *storedAuth, a *auth.Auth) map[string]any {
	entry := map[string]any{"auth_index": f.AuthIndex, "uid": sa.Account.UID, "nickname": sa.Account.Nickname, "_observed_at": time.Now()}
	// v0.12.72 (issue #29 field report): intl accounts live on a DIFFERENT
	// billing face (grow-normal.trae.ai + /trae/api/v1/pay/*). Driving them
	// through the CN client surfaced "ent_usage: upstream session_dead (http
	// 401) code 4014" for every intl account. Route by realm.
	if isIntlStoredAuth(sa) {
		return refreshAccountCreditsIntl(f, sa, entry)
	}
	usage, err := upstreamClient.UserEntUsage(a)
	if err != nil {
		entry["error"] = "ent_usage: " + err.Error()
		// v0.12.75: pay 族 401 code 1001 是 access token 过期/失效的下游症状，
		// 面板此前误导性地只显示 "ent_usage: upstream session_dead"。
		annotateStaleToken(entry, err, a.ExpiresAt, time.Now().Unix())
		return entry
	}
	sum, plan, selected := summarizeEntUsage(usage, true)
	// v0.12.29: ide_user_pay_status —— 上游刷新链路的第二个数据源
	// （trae_account_core_refresh.rs 先 pay_status 再 ent_usage）。Free CN/SOLO
	// 的 ent_usage pack 里没有可解析 quota，剩余维度（快请求/月、SOLO 并发）
	// 只在 pay_status 的 detail/quota 里。best-effort：失败不阻塞 credits。
	if ps, psErr := upstreamClient.PayStatus(a); psErr == nil && ps.Code == 0 {
		sum.FastRequestPer = ps.FastRequestPer()
		sum.SoloParallel = ps.SoloParallelLimit()
		sum.SoloPackage = ps.HasSoloPackage()
		sum.PlanType = ps.PlanIdentity()
		// 选中包缺失时回退 user_pay_identity_str 作为计划显示
		// （上游 account.plan_type 即来自这里）。
		if selected == nil {
			if id := ps.PlanIdentity(); id != "" {
				plan = id
			}
		}
	}
	// v0.12.44: CheckLogin —— 登录态 + 服务端绑定设备探测（best-effort，
	// cockpit-tools 刷新链路同款，trae_account_core_refresh.rs:1035-1045）。
	// BoundDeviceID 与本账号 deviceId 不一致 / DeviceBindStatus != BOUND /
	// IsLogin=false → 签到风控高危（9074 高危画像），日志告警 + 面板亮标。
	bind := &bindStatus{}
	if cl, clErr := upstreamClient.CheckLogin(a); clErr == nil && cl != nil {
		bind.Known = true
		bind.IsLogin = cl.IsLogin
		bind.BoundDeviceID = cl.BoundDeviceID
		bind.DeviceBindStatus = cl.DeviceBindStatus
		bind.DeviceMatch = cl.BoundDeviceID != "" && cl.BoundDeviceID == a.DeviceID
		entry["is_login"] = cl.IsLogin
		if cl.BoundDeviceID != "" {
			entry["bound_device_id"] = cl.BoundDeviceID
			entry["device_bind_status"] = cl.DeviceBindStatus
			entry["device_match"] = bind.DeviceMatch
		}
		if !cl.IsLogin || (cl.DeviceBindStatus != "" && cl.DeviceBindStatus != "BOUND") || (cl.BoundDeviceID != "" && cl.BoundDeviceID != a.DeviceID) {
			log.Printf("checkin device-bind warning uid=%s: isLogin=%v bindStatus=%q bound=%q local=%q — 绑定不一致为 9074 风控高危，建议面板退出重新登录以重绑设备", sa.Account.UID, cl.IsLogin, cl.DeviceBindStatus, cl.BoundDeviceID, a.DeviceID)
		}
	} else if clErr != nil {
		entry["checklogin_error"] = clErr.Error()
	}
	// v0.12.25: also fetch the CHECK-IN status (CN face only — the intl realm
	// has no check-in endpoints; refreshAccountCreditsIntl passes zero values
	// and skips this). v0.12.40: credits 语义修正——它是签到奖励数额（非钱包），
	// 仅作展示与"已签"判定，不再计入池子评分/可花余额。
	wallet := int64(-1)
	checkedIn, enable := false, false
	if st, stErr := upstreamClient.CheckinStatus(a, upstream.CheckinDeviceFor(a)); stErr == nil {
		wallet = st.Credits
		checkedIn, enable = st.CheckedIn || st.DidCheckedIn, st.Enable
	} else {
		entry["checkin_status_error"] = stErr.Error()
	}
	// Shared tail: independent field snapshots; no routing or account scoring.
	return finalizeAccountCredits(f, sa, entry, sum, plan, bind, wallet, checkedIn, enable)
}

// annotateStaleToken flags a credits-query failure whose real root cause is
// an expired access token: when the error carries the session_dead
// signature AND the token's ExpiresAt is already in the past, the entry's
// error gains an actionable suffix and a token_expired=true machine-readable
// marker (v0.12.75 — pay 族 401 code 1001 是 access token 过期/失效的
// 下游症状，面板此前误导性地只显示 "ent_usage: upstream session_dead"，
// 2026-10-12 live 实测定案). Pure function — mutates entry only.
func annotateStaleToken(entry map[string]any, err error, expiresAt, now int64) {
	if err == nil || now <= expiresAt {
		return
	}
	if !strings.Contains(err.Error(), "session_dead") {
		return
	}
	const suffix = "（access token 已过期——请先点顶部「刷新」；若 refresh 报 code 20101 则凭证链已被服务端轮换，需退出账号重新登录）"
	if prev, ok := entry["error"].(string); ok && prev != "" {
		entry["error"] = prev + suffix
	} else {
		entry["error"] = suffix
	}
	entry["token_expired"] = true
}

// summarizeEntUsage builds the panel/cache usage summary, plan label and
// selected pack from an ent_usage snapshot (v0.12.28/34 semantics, shared
// CN/Intl).
func summarizeEntUsage(usage *upstream.EntUsageResult, isCN bool) (upstream.UsageSummary, string, *upstream.EntitlementPack) {
	// v0.12.28: 套餐剩余对齐 cockpit-tools 的用量模型（trae.ts）：
	//   fast  → 速通可用次数（-1 无限）
	//   basic → 选中包 basic_usage_limit - basic_usage_amount（含 bonus）
	//   unknown → 剩余不可知（面板显示 "--"；旧代码读不存在的
	//             credits_limit 字段把这里渲染成"剩余 0 积分 · 00%"）。
	sum := upstream.SummarizeUsage(usage.UserEntitlementPackList, isCN)
	// v0.12.34: 官方 cashier 同口径积分池（Σ max(credits_limit-usage,0)，
	// -1 不限）。这是模型调用真正扣减的池子——此前把签到钱包当
	// "剩余积分"展示，与官方数字对不上（用户实测反馈）。
	sum.CreditsPool = upstream.CreditsPoolUsage(usage.UserEntitlementPackList, usage.IsCreditsBilling)
	selected := upstream.SelectActivePack(usage.UserEntitlementPackList, isCN)
	plan := "Unknown"
	if selected != nil {
		// 上游 identityStr 优先取选中包 display_desc，回退 product_type 映射。
		if d := strings.TrimSpace(selected.DisplayDesc); d != "" {
			plan = d
		} else {
			plan = upstream.ProductTypeIdentity(selected.EntitlementBaseInfo.ProductType, true)
		}
	}
	return sum, plan, selected
}

// finalizeAccountCredits projects the usage summary onto the management entry
// and merges the independent accountCache fields. Shared by
// the CN/SOLO and Intl data paths (v0.12.72 extraction — behavior-preserving
// for CN/SOLO).
func finalizeAccountCredits(f pluginapi.HostAuthFileEntry, sa *storedAuth, entry map[string]any, sum upstream.UsageSummary, plan string, bind *bindStatus, wallet int64, checkedIn, enable bool) map[string]any {
	entry["usage_model"] = sum.UsageModel
	entry["remain_known"] = sum.RemainKnown
	if sum.RemainKnown {
		entry["total_remain"] = sum.Remain
	} else {
		entry["total_remain"] = nil // Unknown is not an observed zero.
	}
	entry["plan"] = plan
	// v0.12.34: 积分池透出（面板"剩余积分"对齐官方口径）。
	entry["credits_pool_known"] = sum.CreditsPool.Known
	if sum.CreditsPool.Known {
		entry["credits_pool_remain"] = sum.CreditsPool.Remain
		entry["credits_pool_unlimited"] = sum.CreditsPool.Unlimited
		if sum.CreditsPool.BreakdownKnown && !sum.CreditsPool.Unlimited {
			entry["credits_pool_total"] = sum.CreditsPool.Total
			entry["credits_pool_used"] = sum.CreditsPool.Used
			entry["credits_pool_pack_count"] = sum.CreditsPool.PackCount
		}
	}
	if sum.UsageModel == "basic" {
		entry["used"] = sum.Used
		entry["total"] = sum.Total
	}
	if sum.UsageModel == "fast" {
		entry["fast_limit"] = sum.FastLimit
		entry["fast_used"] = sum.FastUsed
	}
	// v0.12.29: pay_status 补充维度透传（面板"快请求/月 / Solo 并发"）。
	if sum.FastRequestPer != nil {
		entry["fast_request_per"] = *sum.FastRequestPer
	}
	if sum.SoloParallel != nil {
		entry["solo_parallel"] = *sum.SoloParallel
	}
	if sum.SoloPackage {
		entry["solo_package"] = true
	}
	if sum.PlanType != "" {
		entry["plan_type"] = sum.PlanType
	}
	if wallet >= 0 {
		entry["checkin_credits"] = wallet
		entry["checked_in"] = checkedIn
	}
	observed, ok := entry["_observed_at"].(time.Time)
	if !ok {
		observed = time.Now()
	}
	delete(entry, "_observed_at")
	updateUsageCache(f.AuthIndex, observed, sum, plan, bind, wallet, checkedIn, enable)
	usageQuotaStampFromSummary(f.AuthIndex, sum)
	// A zero/positive balance is only an observation: neither disable accounts
	// nor clear unrelated upstream quota/model/rate-limit failures.

	return entry
}

// refreshAccountCreditsIntl is the intl-realm data path (v0.12.72, issue #29
// field report): ent_usage + pay_status on the intl pay face (intl_pay.go —
// grow-normal.trae.ai + /trae/api/v1/pay/*). No check-in / CheckLogin: those
// are CN-face endpoints and upstream 404s/401s them on intl. The cache
// snapshot does not participate in routing.
func refreshAccountCreditsIntl(f pluginapi.HostAuthFileEntry, sa *storedAuth, entry map[string]any) map[string]any {
	deviceID := sa.Auth.DeviceID
	if deviceID == "" {
		deviceID = sa.Auth.BoundDeviceID
	}
	usageResp, err := intlEntUsage(sa.Auth.AccessToken, sa.Auth.Region, deviceID)
	if err != nil {
		entry["error"] = "intl ent_usage: " + err.Error()
		// v0.12.75: 与 CN 路径同一把尺——过期 token 的 session_dead 失败附上
		// 先刷新/需重登的指引与 token_expired 标记。
		annotateStaleToken(entry, err, sa.Auth.ExpiresAt, time.Now().Unix())
		return entry
	}
	usage := usageResp.toCNResult()
	sum, plan, selected := summarizeEntUsage(usage, false)
	// pay_status v1 — best-effort, same summary fields as the CN face.
	if raw, psErr := intlPayStatusRaw(sa.Auth.AccessToken, sa.Auth.Region, deviceID); psErr == nil {
		var ps upstream.PayStatusResult
		if json.Unmarshal(raw, &ps) == nil && ps.Code == 0 {
			sum.FastRequestPer = ps.FastRequestPer()
			sum.SoloParallel = ps.SoloParallelLimit()
			sum.SoloPackage = ps.HasSoloPackage()
			sum.PlanType = ps.PlanIdentity()
			if selected == nil && ps.PlanIdentity() != "" {
				plan = ps.PlanIdentity()
			}
		}
	}
	entry["variant"] = "intl"
	// No check-in on intl: wallet=-1 keeps checkin fields out of the entry.
	return finalizeAccountCredits(f, sa, entry, sum, plan, &bindStatus{}, -1, false, false)
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// handleRefresh forces an ExchangeToken refresh on every account whose token
// is within the refresh skew of expiry. Updates host-stored auth via host.auth.save
// is the host's responsibility (we only mutate the in-memory Auth and trigger
// upstream refresh); the next host.auth.refresh RPC will persist the new tokens.
func refreshAccount(index string, refreshToken bool) map[string]any {
	unlock := accountOperationLock(index)
	defer unlock()
	entry := map[string]any{"auth_index": index}
	s, err := freshAuthSnapshot(index)
	if err != nil {
		entry["error"] = err.Error()
		return entry
	}
	if s.File.Disabled {
		entry["error"] = "account is manually disabled"
		return entry
	}
	entry["uid"], entry["nickname"] = s.Auth.UID, s.Auth.Nickname
	if refreshToken {
		refreshed, err := refreshSnapshot(s, defaultRefreshSkew)
		if err != nil {
			entry["error"] = "refresh: " + err.Error()
			return entry
		}
		entry["refreshed"] = refreshed
	}
	ce := refreshAccountCredits(s.File, s.Stored, s.Auth)
	for k, v := range ce {
		if k != "_observed_at" {
			entry[k] = v
		}
	}
	return entry
}
func handleRefresh() map[string]any {
	files, err := maintenanceAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	results := []map[string]any{}
	for _, f := range files {
		results = append(results, refreshAccount(f.AuthIndex, true))
	}
	return map[string]any{"provider": providerName, "results": results, "accounts": buildDashboard()["accounts"], "server_time": time.Now().Format(time.RFC3339)}
}

// hostAuthSave persists credential JSON via host.auth.save RPC.
// Used by executor to write back refreshed tokens so they survive CPA restart.
func hostAuthSave(name string, raw []byte) error {
	// v0.12.63: typed rebuilds (intlpersistRefreshedAuthTo, import) construct
	// a fresh document and would silently drop the persisted model_cache
	// snapshot. Re-inject whitelisted plugin-stamped keys the caller didn't
	// set but the physical file carries.
	raw = preservePluginDocKeys(name, raw)
	// Use pluginapi.HostAuthSaveRequest so JSON field (json.RawMessage) is
	// embedded raw, NOT base64-encoded (which map[string]any{"json": raw} would do).
	saveReq := pluginapi.HostAuthSaveRequest{
		Name: name,
		JSON: raw,
	}
	saveBody, _ := json.Marshal(saveReq)
	rawResp, err := hostCall(pluginabi.MethodHostAuthSave, saveBody)
	if err != nil {
		return fmt.Errorf("host.auth.save RPC: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(rawResp, &env); err != nil || !env.OK {
		return fmt.Errorf("host.auth.save: bad envelope")
	}
	return nil
}

// storageJSONForAuth 构建嵌套形 credential JSON（host.auth.save 载荷）。
// type/provider 字段必须有——CPA 按它们把文件路由给本插件（v0.12.6+）。
func storageJSONForAuth(a *auth.Auth) []byte {
	storageJSON, _ := json.MarshalIndent(map[string]any{
		"type":     providerName,
		"provider": providerName,
		"auth": map[string]any{
			"accessToken":  a.AccessToken,
			"refreshToken": a.RefreshToken,
			"expiresAt":    a.ExpiresAt,
			"domain":       a.Domain,
			"apiHost":      a.APIHost,
			"machineId":    a.MachineID,
			"deviceId":     a.DeviceID,
			// v0.12.6: keep the parsed variant — dropping it made a solo
			// account degrade to cn on refresh/import.
			"variant": a.Variant,
			// v0.12.73: 设备密钥对随宿主副本回写 —— 绑定续期靠它签名，丢了
			// 就只能走 /cloudide 裸续期（9074 根因）。
			"devicePublicKey":  a.DevicePublicKey,
			"devicePrivateKey": a.DevicePrivateKey,
		},
		"account": map[string]any{
			"uid":          a.UID,
			"enterpriseId": a.EnterpriseID,
			"nickname":     a.Nickname,
		},
	}, "", "  ")
	return storageJSON
}

// mergeAuthStorage v0.12.44 — refresh 写回从"按内存结构重建"改为"合并进现有
// 凭证 JSON"。此前 handleRefreshAuth / persistRefreshedAuth 每次刷新都用
// auth.Auth（归一化固定字段）重建 storageJSON，把登录时落盘的
// devicePublicKey/devicePrivateKey（v0.12.24 设备绑定密钥对！）以及 v0.12.44
// 的 exchangeResponse/profileRaw/region 回显等 parity 字段整体抹掉。
// 合并策略：token 类字段用刷新后的新值覆盖，其余键（设备密钥对、平台谱系、
// 绑定设备、profile 富字段……）原样保留；existing 不可解析或缺 auth 块时
// 回退旧的重建行为（兼容 legacy 扁平形）。
func mergeAuthStorage(existing []byte, a *auth.Auth) []byte {
	base := map[string]any{}
	if err := json.Unmarshal(existing, &base); err != nil {
		return storageJSONForAuth(a)
	}
	authMap, ok := base["auth"].(map[string]any)
	if !ok {
		authMap = base
	}
	authMap["accessToken"] = a.AccessToken
	authMap["refreshToken"] = a.RefreshToken
	authMap["expiresAt"] = a.ExpiresAt
	authMap["domain"] = a.Domain
	authMap["apiHost"] = a.APIHost
	if a.MachineID != "" {
		authMap["machineId"] = a.MachineID
	}
	if a.DeviceID != "" {
		authMap["deviceId"] = a.DeviceID
	}
	if a.Variant != "" {
		authMap["variant"] = a.Variant
	}
	accountMap, okAcc := base["account"].(map[string]any)
	if !okAcc {
		accountMap = map[string]any{}
		base["account"] = accountMap
	}
	if a.UID != "" {
		accountMap["uid"] = a.UID
	}
	if a.Nickname != "" {
		accountMap["nickname"] = a.Nickname
	}
	if a.EnterpriseID != "" {
		accountMap["enterpriseId"] = a.EnterpriseID
	}
	base["type"] = providerName
	base["provider"] = providerName
	out, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return storageJSONForAuth(a)
	}
	return out
}

// handleImportAuth imports a Trae credential JSON (nested or flat) into the
// host auth store. Body: {"json": <raw json>} or {"raw": "<json string>"}.
// This lets users paste a token from another tool (e.g. traework2api login.sh)
// without going through the browser OAuth flow.
func handleImportAuth(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		JSON json.RawMessage `json:"json"`
		Raw  string          `json:"raw"`
	}
	_ = json.Unmarshal(req.Body, &body)
	raw := []byte(strings.TrimSpace(body.Raw))
	if len(body.JSON) > 0 {
		raw = body.JSON
	}
	if len(raw) == 0 {
		return map[string]any{"success": false, "error": "missing json/raw credential payload"}
	}
	a, err := auth.Parse(raw)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	// Build nested storage JSON for host.auth.save (v0.12.38: shared builder).
	storageJSON := storageJSONForAuth(a)
	// v0.12.27: per-variant namespace — an imported solo credential must
	// not overwrite the cn file of the same Trae account.
	fileName := credentialFileName(a.Variant, a.UID)
	if err := hostAuthSave(fileName, storageJSON); err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	return map[string]any{
		"success":  true,
		"name":     fileName,
		"uid":      a.UID,
		"nickname": a.Nickname,
	}
}
