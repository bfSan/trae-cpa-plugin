package main

import (
	"errors"
	"net/url"
	"time"
)

// register serializes map admission and persistence; a new tab never evicts a
// different login. Only expired contexts are pruned and listeners closed.
func registerLoginCN(lc *loginCtx) error {
	restorePendingMu.Lock()
	defer restorePendingMu.Unlock()
	if !admitLogin() {
		return errors.New("too many pending logins; wait for expiry")
	}
	if err := checkpointCN(lc); err != nil {
		return err
	}
	loginStates.Store(lc.state, lc)
	return nil
}
func registerLoginIntl(lc *intlloginCtx) error {
	restorePendingMu.Lock()
	defer restorePendingMu.Unlock()
	if !admitLogin() {
		return errors.New("too many pending logins; wait for expiry")
	}
	if err := checkpointIntl(lc); err != nil {
		return err
	}
	intlloginStates.Store(lc.state, lc)
	return nil
}
func admitLogin() bool {
	count := 0
	now := time.Now()
	loginStates.Range(func(k, v any) bool {
		lc := v.(*loginCtx)
		if lc.mu.TryLock() {
			if !now.Before(lc.expires) {
				closeListener(lc.listener)
				loginStates.CompareAndDelete(k, v)
				clearPendingLogin(lc.authDir, lc.state)
			} else {
				count++
			}
			lc.mu.Unlock()
		} else {
			count++
		}
		return true
	})
	intlloginStates.Range(func(k, v any) bool {
		lc := v.(*intlloginCtx)
		if lc.mu.TryLock() {
			if !now.Before(lc.expires) {
				closeListener(lc.listener)
				intlloginStates.CompareAndDelete(k, v)
				clearPendingLogin(lc.authDir, lc.state)
			} else {
				count++
			}
			lc.mu.Unlock()
		} else {
			count++
		}
		return true
	})
	return count < oauthPendingMaxFiles
}
func explicitCallbackState(vals url.Values) string {
	for _, k := range []string{"state", "loginTraceID", "loginTraceId", "login_trace_id", "trace_id"} {
		if s := vals.Get(k); s != "" {
			return s
		}
	}
	return ""
}
func acceptLoginCallbackCN(lc *loginCtx, vals url.Values) (string, bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if !time.Now().Before(lc.expires) {
		return "login expired; start a new login", true
	}
	if s := explicitCallbackState(vals); s != "" && s != lc.state {
		return "callback state mismatch", true
	}
	// First callback wins. Re-paste can retry checkpoint/save, never mutate a
	// bound device, callback grant, or already received token.
	if lc.ExchangeStarted || lc.authCode != "" || lc.refreshToken != "" {
		if err := checkpointCN(lc); err != nil {
			return err.Error(), true
		}
		return "", true
	}
	p := parseCallbackParams(vals)
	if p.err != nil {
		lc.err = errors.New("oauth callback rejected")
		return lc.err.Error(), true
	}
	if p.authCode == "" && p.refreshToken == "" {
		return "", false
	}
	lc.authCode, lc.refreshToken = p.authCode, p.refreshToken
	lc.userTag = p.userTag
	if p.loginHost != "" {
		lc.loginHost = p.loginHost
	}
	lc.cbUID, lc.cbNickname = p.identity.UID, p.identity.Nickname
	if lc.cbUID != "" {
		applyDerivedIdentityCN(lc, lc.cbUID)
	}
	completeLogin(lc)
	if err := checkpointCN(lc); err != nil {
		return err.Error(), true
	}
	return "", true
}
func acceptLoginCallbackIntl(lc *intlloginCtx, vals url.Values) (string, bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if !time.Now().Before(lc.expires) {
		return "login expired; start a new login", true
	}
	if s := explicitCallbackState(vals); s != "" && s != lc.state {
		return "callback state mismatch", true
	}
	if lc.ExchangeStarted || lc.authCode != "" {
		if err := checkpointIntl(lc); err != nil {
			return err.Error(), true
		}
		return "", true
	}
	p := parseCallbackParams(vals)
	if p.err != nil {
		lc.err = errors.New("oauth callback rejected")
		return lc.err.Error(), true
	}
	if p.authCode == "" {
		if p.refreshToken != "" {
			return "oauth callback: no authCode received; paste the complete callback URL", true
		}
		return "", false
	}
	lc.authCode = p.authCode
	lc.userTag = p.userTag
	if p.loginHost != "" {
		lc.loginHost = p.loginHost
	}
	lc.cbUID, lc.cbNickname = p.identity.UID, p.identity.Nickname
	// The authorization page supplied the initial DeviceID to GetPCAuthCode.
	// Keep that device, machine and key pair through exchange/save/refresh;
	// userInfo identifies the account, it does not authorize rebinding its code.
	intlCompleteLogin(lc)
	if err := checkpointIntl(lc); err != nil {
		return err.Error(), true
	}
	return "", true
}
