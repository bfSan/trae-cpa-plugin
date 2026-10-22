package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	intlupstream "github.com/bfSan/trae-cpa-plugin/plugins/trae/intlupstream"
	cnupstream "github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type oauthTestTransport func(*http.Request) (*http.Response, error)

func (f oauthTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func oauthResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func oauthTestContext(t *testing.T, state string) *loginCtx {
	t.Helper()
	dir := t.TempDir()
	setAuthDirCacheForTest(t, dir)
	pub, priv, err := generateDeviceKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	lc := &loginCtx{state: state, variant: variantCN, authDir: dir, codeVerifier: strings.Repeat("v", 43), codeChallenge: "challenge", deviceID: "device", machineID: "machine", devicePublicKey: pub, devicePrivateKey: priv, expires: time.Now().Add(loginTTL)}
	lc.CreatedAt = time.Now().Unix()
	lc.authCode = "one-time-secret"
	if err := registerLoginCN(lc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		loginStates.Delete(state)
		pendingStores.Lock()
		if s := pendingStores.stores[dir]; s != nil {
			s.close()
			delete(pendingStores.stores, dir)
		}
		pendingStores.Unlock()
	})
	return lc
}
func mockOAuthFlow(t *testing.T) (*atomic.Int32, *atomic.Int32) {
	t.Helper()
	var exchanges, lookups atomic.Int32
	oldDo, oldCN, oldIntl := oauthHTTPDo, upstreamClient, intlupstreamClient
	upstreamClient = cnupstream.New()
	intlupstreamClient = intlupstream.New()
	oauthHTTPDo = func(req *http.Request) (*http.Response, error) {
		if _, ok := req.Context().Deadline(); !ok {
			t.Error("missing OAuth deadline")
		}
		exchanges.Add(1)
		return oauthResponse(`{"Result":{"AccessToken":"access-secret","RefreshToken":"refresh-secret","TokenExpireAt":2000000000}}`), nil
	}
	upstreamClient.HTTP = &http.Client{Transport: oauthTestTransport(func(req *http.Request) (*http.Response, error) {
		lookups.Add(1)
		return oauthResponse(`{"Result":{"UserID":"uid-42","ScreenName":"mock"}}`), nil
	})}
	t.Cleanup(func() { oauthHTTPDo = oldDo; upstreamClient = oldCN; intlupstreamClient = oldIntl })
	return &exchanges, &lookups
}
func TestOAuthTokenRetainedAfterSaveFailureAndRestart(t *testing.T) {
	exchanges, _ := mockOAuthFlow(t)
	lc := oauthTestContext(t, "recover-save-1")
	setAuthDirCacheForTest(t, lc.authDir)
	old := loginSaveResult
	calls := 0
	loginSaveResult = func(string, *pluginapi.AuthData) error {
		calls++
		if calls == 1 {
			return errors.New("secret access-secret token refresh-secret")
		}
		return nil
	}
	t.Cleanup(func() { loginSaveResult = old })
	if _, err := advanceLoginCN(lc, true); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("save failure not sanitized: %v", err)
	}
	if lc.Token == nil || lc.Result == nil || lc.Saved {
		t.Fatal("token/result checkpoint lost")
	}
	loginStates.Delete(lc.state)
	if restorePendingLoginState(lc.state) != lc.state {
		t.Fatal("could not restore")
	}
	v, _ := loginStates.Load(lc.state)
	restored := v.(*loginCtx)
	if restored.devicePrivateKey != lc.devicePrivateKey {
		t.Fatal("device private key lost")
	}
	out, err := advanceLoginCN(restored, true)
	if err != nil || out.Status != pluginapi.AuthLoginStatusSuccess || !restored.Saved {
		t.Fatalf("retry failed: %v", err)
	}
	if exchanges.Load() != 1 || calls != 2 {
		t.Fatalf("exchange/save count %d/%d", exchanges.Load(), calls)
	}
	if _, err := advanceLoginCN(restored, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("saved state resaved")
	}
}
func TestOAuthIdentityFailureRetainsToken(t *testing.T) {
	exchanges, _ := mockOAuthFlow(t)
	lc := oauthTestContext(t, "recover-identity-1")
	upstreamClient.HTTP = &http.Client{Transport: oauthTestTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret upstream") })}
	if _, err := advanceLoginCN(lc, false); err == nil {
		t.Fatal("identity failure should retry")
	}
	if lc.Token == nil || lc.Result != nil {
		t.Fatal("invalid token checkpoint")
	}
	lc.cbUID = "safe-callback-uid"
	if _, err := advanceLoginCN(lc, false); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 1 {
		t.Fatal("re-exchanged code")
	}
}
func TestOAuthConcurrentPollAndSelfCompleteSingleExchange(t *testing.T) {
	exchanges, _ := mockOAuthFlow(t)
	lc := oauthTestContext(t, "concurrent-1")
	old := loginSaveResult
	var saves atomic.Int32
	loginSaveResult = func(string, *pluginapi.AuthData) error { saves.Add(1); return nil }
	t.Cleanup(func() { loginSaveResult = old })
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(self bool) { defer wg.Done(); _, _ = advanceLoginCN(lc, self) }(i%2 == 0)
	}
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("exchanges=%d", exchanges.Load())
	}
	if saves.Load() > 1 {
		t.Fatalf("saves=%d", saves.Load())
	}
	if lc.Owner == "host" && saves.Load() != 0 {
		t.Fatal("host-owned result self-saved")
	}
}
func TestOAuthHostDeliveryDoesNotClaimSaved(t *testing.T) {
	mockOAuthFlow(t)
	lc := oauthTestContext(t, "host-replay-1")
	first, err := advanceLoginCN(lc, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := advanceLoginCN(lc, false)
	if err != nil || string(first.Auth.StorageJSON) != string(second.Auth.StorageJSON) {
		t.Fatal("host result not replayed")
	}
	if lc.Saved || lc.Owner != "host" {
		t.Fatal("host delivery must not mean saved")
	}
	old := loginSaveResult
	loginSaveResult = func(string, *pluginapi.AuthData) error { t.Error("host result saved twice"); return nil }
	defer func() { loginSaveResult = old }()
	_, _ = advanceLoginCN(lc, true)
}
func TestOAuthCheckpointFailureDoesNotReexchange(t *testing.T) {
	var p loginProgress
	checks, exchanges := 0, 0
	checkpoint := func() error {
		checks++
		if checks == 2 {
			return errOAuthStorage
		}
		return nil
	}
	exchange := func() (*loginToken, error) { exchanges++; return &loginToken{Access: "secret"}, nil }
	if err := exchangeOnce(&p, checkpoint, exchange); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	if p.Token == nil {
		t.Fatal("token was discarded")
	}
	if err := exchangeOnce(&p, checkpoint, exchange); err != nil {
		t.Fatal(err)
	}
	if exchanges != 1 {
		t.Fatal("re-exchanged token")
	}
}
func TestOAuthAmbiguousExchangeNeverResubmits(t *testing.T) {
	var p loginProgress
	count := 0
	exchange := func() (*loginToken, error) { count++; return nil, errors.New("secret in upstream URL") }
	for i := 0; i < 2; i++ {
		err := exchangeOnce(&p, func() error { return nil }, exchange)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("error=%v", err)
		}
	}
	if count != 1 {
		t.Fatalf("submissions=%d", count)
	}
}
func TestOAuthMultipleStatesRestoreAndDeleteIsolation(t *testing.T) {
	a := oauthTestContext(t, "multi-a")
	b := recordCN(a)
	b.State = "multi-b"
	if err := persistPendingLogin(b); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPendingLogin(a.authDir); ok {
		t.Fatal("ambiguous bare callback adopted a state")
	}
	clearPendingLogin(a.authDir)
	if _, ok := loadPendingLoginState(a.authDir, a.state); !ok {
		t.Fatal("stateless clear removed another login")
	}
	clearPendingLogin(a.authDir, a.state)
	if _, ok := loadPendingLoginState(a.authDir, b.State); !ok {
		t.Fatal("cleared different state")
	}
}
func TestOAuthPendingTamperingAndSymlinkRejected(t *testing.T) {
	lc := oauthTestContext(t, "tamper-1")
	path := filepath.Join(pendingLoginPath(lc.authDir), oauthPendingName(lc.state))
	if err := os.WriteFile(path, []byte(`{"record":{"state":"tamper-1"},"mac":"00"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPendingLoginState(lc.authDir, lc.state); ok {
		t.Fatal("accepted modified record")
	}
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, pendingLoginFileName)); err != nil {
		t.Fatal(err)
	}
	rec := testPendingRecord("symlink-1")
	rec.AuthDir = dir
	if err := persistPendingLogin(rec); err == nil {
		t.Fatal("accepted symlink store")
	}
}
func TestOAuthCallbackFirstGrantAndErrorRedaction(t *testing.T) {
	lc := oauthTestContext(t, "callback-guard-1")
	first := lc.authCode
	msg, ok := acceptLoginCallbackCN(lc, url.Values{"state": {lc.state}, "authCode": {"different-secret"}, "userInfo": {`{"UserID":"different"}`}})
	if !ok || msg != "" || lc.authCode != first || lc.cbUID != "" {
		t.Fatal("duplicate callback changed grant/device")
	}
	p := parseCallbackParams(url.Values{"error": {"access-secret"}})
	if p.err == nil || strings.Contains(p.err.Error(), "access-secret") {
		t.Fatal("callback error leaked")
	}
}
func TestOAuthStatusExcludesCredentials(t *testing.T) {
	mockOAuthFlow(t)
	lc := oauthTestContext(t, "status-1")
	_, _ = advanceLoginCN(lc, false)
	raw, _ := json.Marshal(oauthLoginStatus(lc.state))
	for _, secret := range []string{"access-secret", "refresh-secret", "one-time-secret", "PRIVATE KEY"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("status exposed credentials")
		}
	}
}
func TestOAuthIntlTokenSaveRecovery(t *testing.T) {
	exchanges, _ := mockOAuthFlow(t)
	cn := oauthTestContext(t, "intl-save-1")
	loginStates.Delete(cn.state)
	lc := &intlloginCtx{state: cn.state, authDir: cn.authDir, codeVerifier: cn.codeVerifier, deviceID: cn.deviceID, machineID: cn.machineID, devicePublicKey: cn.devicePublicKey, devicePrivateKey: cn.devicePrivateKey, expires: cn.expires, authCode: cn.authCode}
	lc.CreatedAt = cn.CreatedAt
	clearPendingLogin(cn.authDir, cn.state)
	if err := registerLoginIntl(lc); err != nil {
		t.Fatal(err)
	}
	defer intlloginStates.Delete(lc.state)
	oldHTTP := intlupstreamClient.HTTP
	intlupstreamClient.HTTP = &http.Client{Transport: oauthTestTransport(func(*http.Request) (*http.Response, error) {
		return oauthResponse(`{"Result":{"UserID":"intl-uid","ScreenName":"mock"}}`), nil
	})}
	defer func() { intlupstreamClient.HTTP = oldHTTP }()
	oldSave := loginSaveResult
	calls := 0
	loginSaveResult = func(string, *pluginapi.AuthData) error {
		calls++
		if calls == 1 {
			return errors.New("secret")
		}
		return nil
	}
	defer func() { loginSaveResult = oldSave }()
	if _, err := advanceLoginIntl(lc, true); err == nil {
		t.Fatal("expected save failure")
	}
	if _, err := advanceLoginIntl(lc, true); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 1 || calls != 2 || !lc.Saved {
		t.Fatal("Intl recovery repeated exchange or lost save")
	}
	var stored map[string]any
	if json.Unmarshal(lc.Result.StorageJSON, &stored) != nil {
		t.Fatal("invalid result")
	}
	fields := stored["auth"].(map[string]any)
	if fields["devicePrivateKey"] != lc.devicePrivateKey || fields["deviceId"] != lc.deviceID {
		t.Fatal("Intl lost device binding")
	}
}

func TestOAuthSecureStoreExclusiveLockAndReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	store, err := openOAuthPendingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := openOAuthPendingStore(dir); err == nil {
		duplicate.close()
		t.Fatal("second process lock accepted")
	}
	rec := testPendingRecord("reopen-1")
	if err := store.write(&rec); err != nil {
		t.Fatal(err)
	}
	store.close()
	restored, err := openOAuthPendingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	if got, err := restored.read(oauthPendingName(rec.State)); err != nil || got.State != rec.State {
		t.Fatal("record lost on reopen")
	}
}
func TestOAuthStoreRejectsUnsafeModesAndOversizedRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	store, err := openOAuthPendingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	rec := testPendingRecord("permissions-1")
	if err := store.write(&rec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, oauthPendingName(rec.State))
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read(oauthPendingName(rec.State)); err == nil {
		t.Fatal("world-readable token checkpoint accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	rec.AuthCode = strings.Repeat("x", oauthPendingMaxBytes)
	if err := store.write(&rec); err == nil {
		t.Fatal("oversized record accepted")
	}
}
func TestOAuthNoStateIncludesUnrestoredDiskLogin(t *testing.T) {
	lc := oauthTestContext(t, "anonymous-live-1")
	lc.mu.Lock()
	lc.authCode = ""
	if err := checkpointCN(lc); err != nil {
		t.Fatal(err)
	}
	lc.mu.Unlock()
	setAuthDirCacheForTest(t, lc.authDir)
	other := recordCN(lc)
	other.State = "anonymous-disk-2"
	if err := persistPendingLogin(other); err != nil {
		t.Fatal(err)
	}
	if s := singleInflightLoginState(); s != "" {
		t.Fatalf("ambiguous callback selected %q", s)
	}
}
func TestOAuthExchangeResponseErrorRedaction(t *testing.T) {
	old := oauthHTTPDo
	defer func() { oauthHTTPDo = old }()
	count := 0
	oauthHTTPDo = func(*http.Request) (*http.Response, error) {
		count++
		r := oauthResponse(`{"ResponseMetadata":{"Error":{"Code":"secret-code","Message":"refresh-secret private-key"}}}`)
		r.StatusCode = 500
		return r, nil
	}
	_, err := exchangeTokenCandidates([]string{"https://api.trae.cn/x", "https://api.trae.com.cn/x"}, []byte(`{}`))
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-key") {
		t.Fatalf("unsafe error %v", err)
	}
	if count != 1 {
		t.Fatal("retried one-time exchange")
	}
}
