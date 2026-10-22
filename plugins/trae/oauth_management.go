package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Register ONLY as authenticated management routes, never resource handlers.
// Responses deliberately omit URL queries, grants, token and AuthData.
func handleOAuthManagementStart(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	var input struct {
		Variant string `json:"variant"`
	}
	if json.Unmarshal(req.Body, &input) != nil {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid login request"})
	}
	v := strings.ToLower(strings.TrimSpace(input.Variant))
	if v == "solo" {
		v = variantSolo
	}
	if v != "cn" && v != variantSolo && v != "intl" {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "variant must be cn, solo or intl"})
	}
	dir := cachedAuthDir()
	if dir == "" {
		return mgmtJSONResponse(http.StatusServiceUnavailable, map[string]any{"error": "login AuthDir unavailable; wait for host initialization"})
	}
	request, _ := json.Marshal(map[string]any{"Host": map[string]string{"AuthDir": dir}, "LoginOwner": "self"})
	var raw []byte
	var err error
	if v == variantIntl {
		raw, err = intlhandleStartLogin(request)
	} else {
		raw, err = startLoginWithVariant(request, v)
	}
	if err != nil {
		return mgmtJSONResponse(http.StatusServiceUnavailable, map[string]any{"error": "login start unavailable; check persistence, callback port and routing"})
	}
	var env struct {
		Result pluginapi.AuthLoginStartResponse
	}
	if json.Unmarshal(raw, &env) != nil || env.Result.State == "" {
		return mgmtJSONResponse(http.StatusInternalServerError, map[string]any{"error": "login start unavailable"})
	}
	r := env.Result
	return mgmtJSONResponse(http.StatusOK, map[string]any{"state": r.State, "url": r.URL, "expires_at": r.ExpiresAt, "variant": v, "stage": "pending", "persistent": true, "retryable": true, "fallback_submit_path": resourceSubmitPath, "fallback_callback_path": resourceCallbackPath})
}
func handleOAuthManagementPoll(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	var input struct {
		State string `json:"state"`
	}
	if json.Unmarshal(req.Body, &input) != nil || !validOAuthState(input.State) {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid login state"})
	}
	state := input.State
	if !stateIsLive(state) {
		restorePendingLoginState(state)
	}
	var out pluginapi.AuthLoginPollResponse
	var err error
	if v, ok := loginStates.Load(state); ok {
		lc := v.(*loginCtx)
		lc.mu.Lock()
		owner := lc.Owner
		lc.mu.Unlock()
		if owner == "host" {
			return mgmtJSONResponse(http.StatusConflict, map[string]any{"error": "login result belongs to the host"})
		}
		out, err = advanceLoginCN(lc, true)
	} else if v, ok := intlloginStates.Load(state); ok {
		lc := v.(*intlloginCtx)
		lc.mu.Lock()
		owner := lc.Owner
		lc.mu.Unlock()
		if owner == "host" {
			return mgmtJSONResponse(http.StatusConflict, map[string]any{"error": "login result belongs to the host"})
		}
		out, err = advanceLoginIntl(lc, true)
	} else {
		return mgmtJSONResponse(http.StatusNotFound, map[string]any{"error": "unknown or expired login state"})
	}
	payload := oauthLoginStatus(state)
	response := map[string]any{"state": state, "status": out.Status, "stage": payload.Stage, "persistent": payload.Persistent, "retryable": payload.Retryable, "saved": payload.Saved, "pending": payload.Pending, "expires_in": payload.TTLRemaining}
	if err != nil {
		response["status"] = "error"
		response["error"] = err.Error()
		return mgmtJSONResponse(http.StatusOK, response)
	}
	return mgmtJSONResponse(http.StatusOK, response)
}
func handleOAuthManagementStatus(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	state := strings.TrimSpace(req.Query.Get("state"))
	if !validOAuthState(state) {
		return mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "state is required"})
	}
	return mgmtJSONResponse(http.StatusOK, oauthLoginStatus(state))
}
func oauthLoginStatus(state string) loginStatusPayload {
	if !stateIsLive(state) {
		restorePendingLoginState(state)
	}
	p := loginStatusPayload{State: state}
	fill := func(progress *loginProgress, dir, variant, cb string, expires time.Time, restored, listener, callback bool) {
		p.Stage = progress.stage()
		p.Persistent = dir != ""
		p.Saved = progress.Saved
		p.Owner = progress.Owner
		p.Variant = variant
		p.CallbackHP = callbackHostPort(cb)
		p.Restored = restored
		p.ListenerAlive = listener
		p.CallbackReceived = callback
		p.TTLRemaining = int64(time.Until(expires).Seconds())
		p.AgeSec = time.Now().Unix() - progress.CreatedAt
		p.Pending = time.Now().Before(expires) && !p.Saved && progress.Owner != "host"
		p.Retryable = time.Now().Before(expires) && !p.Saved && progress.Owner != "host" && (!progress.ExchangeStarted || progress.Token != nil)
	}
	if v, ok := loginStates.Load(state); ok {
		lc := v.(*loginCtx)
		lc.mu.Lock()
		fill(&lc.loginProgress, lc.authDir, lc.variant, lc.cbURL, lc.expires, lc.restored, lc.listener != nil, lc.authCode != "" || lc.refreshToken != "")
		lc.mu.Unlock()
	} else if v, ok := intlloginStates.Load(state); ok {
		lc := v.(*intlloginCtx)
		lc.mu.Lock()
		fill(&lc.loginProgress, lc.authDir, variantIntl, lc.cbURL, lc.expires, lc.restored, lc.listener != nil, lc.authCode != "")
		lc.mu.Unlock()
	}
	if o, ok := lookupLoginOutcome(state); ok {
		p.Outcome = &loginStatusOutcome{OK: o.ok, Message: o.msg, AgeSec: int64(time.Since(o.at).Seconds())}
	}
	return p
}
