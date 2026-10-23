package main

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	intlupstream "github.com/bfSan/trae-cpa-plugin/plugins/trae/intlupstream"
	cnupstream "github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Covers callbacks, checkpoints, exchange and ownership. Host saving is
// external: no cross-system exactly-once guarantee without acknowledgement.
type loginProgress struct {
	selfRunning     bool
	mu              sync.Mutex
	CreatedAt       int64                 `json:"created_at"`
	Owner           string                `json:"owner,omitempty"`
	ExchangeStarted bool                  `json:"exchange_started,omitempty"`
	Token           *loginToken           `json:"token,omitempty"`
	Result          *pluginapi.AuthData   `json:"result,omitempty"`
	Saved           bool                  `json:"saved,omitempty"`
	Failure         string                `json:"failure,omitempty"`
	ExchangeFailure *loginExchangeFailure `json:"exchange_failure,omitempty"`
}
type loginToken struct {
	Access     string          `json:"access"`
	Refresh    string          `json:"refresh"`
	Expires    int64           `json:"expires"`
	Raw        json.RawMessage `json:"raw,omitempty"`
	ReceivedAt int64           `json:"received_at"`
}

func (p *loginProgress) stage() string {
	switch {
	case p.Saved:
		return "saved"
	case p.Result != nil:
		return "result"
	case p.Token != nil:
		return "token"
	case p.ExchangeStarted:
		return "exchange_started"
	default:
		return "pending"
	}
}
func recordCN(lc *loginCtx) pendingLoginRecord {
	return pendingLoginRecord{Flow: "cn", State: lc.state, Variant: lc.variant, LoginHost: lc.loginHost, CbURL: lc.cbURL, CodeVerifier: lc.codeVerifier, CodeChallenge: lc.codeChallenge, DeviceID: lc.deviceID, MachineID: lc.machineID, DevicePublicKey: lc.devicePublicKey, DevicePrivateKey: lc.devicePrivateKey, AuthDir: lc.authDir, CreatedAt: lc.CreatedAt, ExpiresAt: lc.expires.Unix(), AuthCode: lc.authCode, RefreshToken: lc.refreshToken, UserTag: lc.userTag, CBUID: lc.cbUID, CBNickname: lc.cbNickname, Progress: progressCopy(&lc.loginProgress)}
}
func recordIntl(lc *intlloginCtx) pendingLoginRecord {
	return pendingLoginRecord{Flow: "intl", State: lc.state, Variant: variantIntl, LoginHost: lc.loginHost, CbURL: lc.cbURL, CodeVerifier: lc.codeVerifier, CodeChallenge: lc.codeChallenge, DeviceID: lc.deviceID, MachineID: lc.machineID, DevicePublicKey: lc.devicePublicKey, DevicePrivateKey: lc.devicePrivateKey, AuthDir: lc.authDir, CreatedAt: lc.CreatedAt, ExpiresAt: lc.expires.Unix(), AuthCode: lc.authCode, UserTag: lc.userTag, CBUID: lc.cbUID, CBNickname: lc.cbNickname, Progress: progressCopy(&lc.loginProgress)}
}
func progressCopy(p *loginProgress) *loginProgress {
	return &loginProgress{CreatedAt: p.CreatedAt, Owner: p.Owner, ExchangeStarted: p.ExchangeStarted, Token: p.Token, Result: p.Result, Saved: p.Saved, Failure: p.Failure, ExchangeFailure: p.ExchangeFailure.sanitized()}
}
func checkpointCN(lc *loginCtx) error {
	retainLoginToken(&lc.loginProgress, &lc.expires)
	return persistPendingLogin(recordCN(lc))
}
func checkpointIntl(lc *intlloginCtx) error {
	retainLoginToken(&lc.loginProgress, &lc.expires)
	return persistPendingLogin(recordIntl(lc))
}

// Core serializes canonical-account saves and merges fresh manual fields.
// It must not retain a request-scoped host callback in detached self-completion.
var loginSaveResult = func(dir string, result *pluginapi.AuthData) error {
	return safeSaveLoginResult(dir, result)
}

func finishLogin(p *loginProgress, dir, state string, self bool, checkpoint func() error) (pluginapi.AuthLoginPollResponse, error) {
	var out pluginapi.AuthLoginPollResponse
	if p.Result == nil {
		return out, errors.New("login result unavailable")
	}
	if self {
		if p.Owner == "host" {
			return out, nil
		}
		p.Owner = "self"
		if err := checkpoint(); err != nil {
			return out, err
		}
		if !p.Saved {
			if err := loginSaveResult(dir, p.Result); err != nil {
				return out, errors.New("login succeeded but saving failed; retry this state")
			}
			p.Saved = true
		}
	} else if p.Owner == "self" {
		return out, errors.New("login already owned by plugin save; use login status/panel poll")
	} else {
		if err := prepareLoginResult(dir, p.Result); err != nil {
			return out, errors.New("login succeeded but credential metadata unavailable; retry this state")
		}
		p.Owner = "host"
	}
	if err := checkpoint(); err != nil {
		return out, err
	}
	out.Status = pluginapi.AuthLoginStatusSuccess
	out.Message = "login complete"
	out.Auth = *p.Result
	if p.Saved {
		recordLoginOutcome(state, true, "")
	}
	return out, nil
}
func exchangeOnce(p *loginProgress, checkpoint func() error, exchange func() (*loginToken, error)) error {
	if p.Token != nil || p.Result != nil {
		return checkpoint()
	}
	if p.ExchangeStarted {
		if detail := loginProgressExchangeFailure(p); detail != nil {
			return detail
		}
		return &loginExchangeFailure{Category: "outcome_uncertain"}
	}
	p.ExchangeStarted = true
	if err := checkpoint(); err != nil {
		p.ExchangeStarted = false
		return err
	}
	tok, err := exchange()
	if err != nil {
		var detail *loginExchangeFailure
		if errors.As(err, &detail) && detail != nil {
			p.ExchangeFailure = detail.sanitized()
		} else {
			p.ExchangeFailure = &loginExchangeFailure{Category: "outcome_uncertain"}
		}
		p.Failure = p.ExchangeFailure.Error()
		_ = checkpoint()
		return p.ExchangeFailure
	}
	if tok == nil || tok.Access == "" {
		p.ExchangeFailure = &loginExchangeFailure{Category: "missing_access_token"}
		p.Failure = p.ExchangeFailure.Error()
		_ = checkpoint()
		return p.ExchangeFailure
	}
	tok.ReceivedAt = time.Now().Unix()
	p.Token = tok
	// Failure keeps the token in memory. Crash before checkpoint is indeterminate.
	return checkpoint()
}
func advanceLoginCN(lc *loginCtx, self bool) (pluginapi.AuthLoginPollResponse, error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	var out pluginapi.AuthLoginPollResponse
	if !time.Now().Before(lc.expires) {
		return out, errors.New("login expired; start a new login")
	}
	if self && lc.Owner == "host" {
		return out, nil
	}
	if lc.err != nil {
		return out, errors.New("oauth callback rejected; start a new login")
	}
	if lc.Token == nil && lc.authCode == "" && lc.refreshToken == "" {
		out.Status = pluginapi.AuthLoginStatusPending
		out.Message = "waiting for browser login"
		return out, nil
	}
	checkpoint := func() error { return checkpointCN(lc) }
	if lc.CreatedAt == 0 {
		lc.CreatedAt = time.Now().Unix()
	}
	if err := exchangeOnce(&lc.loginProgress, checkpoint, func() (*loginToken, error) {
		pub, priv, err := deviceKeysForLogin(lc.devicePublicKey, lc.devicePrivateKey, lc.authDir, lc.variant)
		if err != nil {
			return nil, errors.New("login device key unavailable")
		}
		lc.devicePublicKey, lc.devicePrivateKey = pub, priv
		if lc.refreshToken != "" {
			a := &auth.Auth{RefreshToken: lc.refreshToken, APIHost: oauthDefaultHost, Domain: "trae.cn", MachineID: lc.machineID, DeviceID: lc.deviceID, DevicePublicKey: pub, DevicePrivateKey: priv, Variant: lc.variant}
			if err := upstreamClient.RefreshToken(a); err != nil {
				return nil, errors.New("login refresh failed")
			}
			return &loginToken{Access: a.AccessToken, Refresh: a.RefreshToken, Expires: a.ExpiresAt}, nil
		}
		body, _ := json.Marshal(map[string]any{"ClientID": cnupstream.ClientIDFor(lc.variant), "AuthCode": lc.authCode, "CodeVerifier": lc.codeVerifier, "DeviceInfo": buildOfficialDeviceInfo(lc.deviceID, lc.machineID, oauthPlatformCodeFor(lc.variant), oauthDeviceName, oauthDeviceBrand, cnupstream.IdeVersion, oauthDeviceType, oauthOSVersion, pub), "IDEVersion": cnupstream.IdeVersion})
		raw, err := exchangeTokenCandidates(authCodeExchangeURLsCN(lc.loginHost), body)
		if err != nil {
			return nil, err
		}
		at, rt, exp := parseExchangeTokenResponse(raw)
		return &loginToken{Access: at, Refresh: rt, Expires: exp, Raw: raw}, nil
	}); err != nil {
		return out, err
	}
	if lc.Result == nil {
		t := lc.Token
		a := &auth.Auth{AccessToken: t.Access, RefreshToken: t.Refresh, ExpiresAt: t.Expires, APIHost: oauthDefaultHost, Domain: "trae.cn", MachineID: lc.machineID, DeviceID: lc.deviceID, Variant: lc.variant, DevicePublicKey: lc.devicePublicKey, DevicePrivateKey: lc.devicePrivateKey}
		uid, nick, ent, profile, err := upstreamClient.GetUserInfoFull(a)
		if err != nil && lc.cbUID == "" {
			return out, errors.New("login identity unavailable; retry this state")
		}
		uid = resolvedSafeLoginUID(uid, lc.cbUID)
		if uid == "" {
			return out, errors.New("login identity unavailable; retry this state")
		}
		nick = resolveLoginNickname(nick, lc.cbNickname)
		fields := map[string]any{"accessToken": t.Access, "refreshToken": t.Refresh, "expiresAt": t.Expires, "apiHost": a.APIHost, "domain": a.Domain, "machineId": lc.machineID, "deviceId": lc.deviceID, "variant": lc.variant, "devicePublicKey": lc.devicePublicKey, "devicePrivateKey": lc.devicePrivateKey}
		account := map[string]any{"uid": uid, "nickname": nick, "enterpriseId": ent}
		extras, ae := credentialParityFields(lc.variant, lc.loginHost, t.Raw, profile)
		for k, v := range extras {
			if _, ok := fields[k]; !ok {
				fields[k] = v
			}
		}
		for k, v := range ae {
			if _, ok := account[k]; !ok {
				account[k] = v
			}
		}
		raw, _ := json.Marshal(map[string]any{"type": providerName, "provider": providerName, "auth": fields, "account": account, "disabled": false})
		name := credentialFileName(lc.variant, uid)
		lc.Result = &pluginapi.AuthData{Provider: providerName, ID: name, FileName: name, Label: nonEmpty(nick, variantLabel(lc.variant)+" "+uid), StorageJSON: raw, Metadata: map[string]any{"type": providerName, "uid": uid, "nickname": nick, "note": authNote(lc.variant)}}
	}
	if err := checkpoint(); err != nil {
		return out, err
	}
	closeListener(lc.listener)
	return finishLogin(&lc.loginProgress, lc.authDir, lc.state, self, checkpoint)
}
func advanceLoginIntl(lc *intlloginCtx, self bool) (pluginapi.AuthLoginPollResponse, error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	var out pluginapi.AuthLoginPollResponse
	if !time.Now().Before(lc.expires) {
		return out, errors.New("login expired; start a new login")
	}
	if self && lc.Owner == "host" {
		return out, nil
	}
	if lc.err != nil {
		return out, errors.New("oauth callback rejected; start a new login")
	}
	if lc.Token == nil && lc.authCode == "" {
		out.Status = pluginapi.AuthLoginStatusPending
		out.Message = "waiting for browser login"
		return out, nil
	}
	checkpoint := func() error { return checkpointIntl(lc) }
	if lc.CreatedAt == 0 {
		lc.CreatedAt = time.Now().Unix()
	}
	if err := exchangeOnce(&lc.loginProgress, checkpoint, func() (*loginToken, error) {
		pub, priv, err := deviceKeysForLogin(lc.devicePublicKey, lc.devicePrivateKey, lc.authDir, variantIntl)
		if err != nil {
			return nil, errors.New("login device key unavailable")
		}
		lc.devicePublicKey, lc.devicePrivateKey = pub, priv
		body, _ := json.Marshal(map[string]any{"ClientID": intlupstreamClient.ClientID, "AuthCode": lc.authCode, "CodeVerifier": lc.codeVerifier, "DeviceInfo": intlbuildOfficialDeviceInfo(lc.deviceID, lc.machineID, oauthPlatformCode, intlOauthDeviceName, intlOauthDeviceBrand, loadedAppVersion(), intlOauthDeviceType, intlOauthOSVersion, pub), "IDEVersion": loadedAppVersion()})
		raw, err := intlexchangeTokenCandidates(intlauthCodeExchangeURLs(lc.loginHost, lc.userTag), body)
		if err != nil {
			return nil, err
		}
		at, rt, exp := parseExchangeTokenResponse(raw)
		return &loginToken{Access: at, Refresh: rt, Expires: exp, Raw: raw}, nil
	}); err != nil {
		return out, err
	}
	if lc.Result == nil {
		t := lc.Token
		a := &intlupstream.Auth{AccessToken: t.Access, RefreshToken: t.Refresh, ExpiresAt: t.Expires, APIHost: intlupstreamClient.OAuthHost, Domain: "trae.ai", Region: "US-East", Scope: "marscode-us", Tenant: "marscode", UserIdentity: "Free", AppLanguage: "en", AppVersion: loadedAppVersion()}
		uid, nick, ent, err := intlupstreamClient.GetUserInfo(a)
		if err != nil && lc.cbUID == "" {
			return out, errors.New("login identity unavailable; retry this state")
		}
		uid = resolvedSafeLoginUID(uid, lc.cbUID)
		if uid == "" {
			return out, errors.New("login identity unavailable; retry this state")
		}
		nick = resolveLoginNickname(nick, lc.cbNickname)
		fields := map[string]any{"accessToken": t.Access, "refreshToken": t.Refresh, "expiresAt": t.Expires, "apiHost": a.APIHost, "domain": a.Domain, "machineId": lc.machineID, "deviceId": lc.deviceID, "variant": variantIntl, "devicePublicKey": lc.devicePublicKey, "devicePrivateKey": lc.devicePrivateKey, "region": a.Region, "scope": a.Scope, "tenant": a.Tenant, "appLanguage": a.AppLanguage, "appVersion": a.AppVersion}
		account := map[string]any{"uid": uid, "nickname": nick, "enterpriseId": ent}
		extras, ae := credentialParityFields(variantIntl, lc.loginHost, t.Raw, nil)
		for k, v := range extras {
			if _, ok := fields[k]; !ok {
				fields[k] = v
			}
		}
		for k, v := range ae {
			if _, ok := account[k]; !ok {
				account[k] = v
			}
		}
		raw, _ := json.Marshal(map[string]any{"type": intlproviderName, "provider": intlproviderName, "auth": fields, "account": account, "disabled": false})
		name := credentialFileName(variantIntl, uid)
		lc.Result = &pluginapi.AuthData{Provider: intlproviderName, ID: name, FileName: name, Label: nonEmpty(nick, "Trae Intl "+uid), StorageJSON: raw, Metadata: map[string]any{"type": intlproviderName, "uid": uid, "nickname": nick, "note": "INTL"}}
	}
	if err := checkpoint(); err != nil {
		return out, err
	}
	closeListener(lc.listener)
	return finishLogin(&lc.loginProgress, lc.authDir, lc.state, self, checkpoint)
}
func resolvedSafeLoginUID(uid, callback string) string {
	if strings.TrimSpace(uid) == "" {
		uid = callback
	}
	uid = strings.TrimSpace(uid)
	if uid == "" || len(uid) > 128 {
		return ""
	}
	for _, c := range uid {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	return uid
}
func pollLoginCN(request []byte) ([]byte, error) {
	captureAuthDir(request)
	var req pluginapi.AuthLoginPollRequest
	if json.Unmarshal(request, &req) != nil {
		return nil, errors.New("invalid login poll")
	}
	state := strings.TrimSpace(req.State)
	if !stateIsLive(state) {
		restorePendingLoginState(state)
	}
	v, ok := loginStates.Load(state)
	if !ok {
		// Host dispatch only sees live maps. After restart restore the persisted
		// region before deciding which flow owns the state.
		if _, intl := intlloginStates.Load(state); intl {
			return pollLoginIntl(request)
		}
		return nil, errors.New("unknown or expired login state")
	}
	lc := v.(*loginCtx)
	lc.mu.Lock()
	if lc.authCode == "" && lc.refreshToken == "" && lc.Token == nil {
		if code, e, ok := readHostCallbackFile(lc.authDir, state); ok {
			lc.authCode = code
			if e != "" {
				lc.err = errors.New("oauth callback rejected")
			}
		}
	}
	lc.mu.Unlock()
	out, err := advanceLoginCN(lc, false)
	if err != nil {
		return nil, err
	}
	return okEnvelope(out)
}
func pollLoginIntl(request []byte) ([]byte, error) {
	captureAuthDir(request)
	var req pluginapi.AuthLoginPollRequest
	if json.Unmarshal(request, &req) != nil {
		return nil, errors.New("invalid login poll")
	}
	state := strings.TrimSpace(req.State)
	if !stateIsLive(state) {
		restorePendingLoginState(state)
	}
	v, ok := intlloginStates.Load(state)
	if !ok {
		return nil, errors.New("unknown or expired login state")
	}
	lc := v.(*intlloginCtx)
	lc.mu.Lock()
	if lc.authCode == "" && lc.Token == nil {
		if code, e, ok := readHostCallbackFile(lc.authDir, state); ok {
			lc.authCode = code
			if e != "" {
				lc.err = errors.New("oauth callback rejected")
			}
		}
	}
	lc.mu.Unlock()
	out, err := advanceLoginIntl(lc, false)
	if err != nil {
		return nil, err
	}
	return okEnvelope(out)
}

func retainLoginToken(p *loginProgress, expires *time.Time) {
	if p.Token == nil || p.Token.ReceivedAt == 0 {
		return
	}
	end := time.Unix(p.Token.ReceivedAt, 0).Add(loginOutcomeTTL)
	cap := time.Unix(p.CreatedAt, 0).Add(loginTTL + loginOutcomeTTL)
	if end.After(cap) {
		end = cap
	}
	*expires = end
}
