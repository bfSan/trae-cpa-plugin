package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const pendingLoginFileName = ".trae-oauth-pending"
const oauthPendingMaxFiles = 128
const oauthPendingMaxBytes = 128 << 10
const loginOutcomeTTL = 10 * time.Minute

var errOAuthStorage = errors.New("login persistence unavailable; retry this state without restarting")
var loginSelfCompleteGrace = 15 * time.Second

type pendingLoginRecord struct {
	Flow             string         `json:"flow"`
	State            string         `json:"state"`
	Variant          string         `json:"variant,omitempty"`
	LoginHost        string         `json:"login_host,omitempty"`
	CbURL            string         `json:"cb_url,omitempty"`
	CodeVerifier     string         `json:"code_verifier"`
	CodeChallenge    string         `json:"code_challenge,omitempty"`
	DeviceID         string         `json:"device_id"`
	MachineID        string         `json:"machine_id"`
	DevicePublicKey  string         `json:"device_public_key,omitempty"`
	DevicePrivateKey string         `json:"device_private_key,omitempty"`
	AuthDir          string         `json:"-"`
	CreatedAt        int64          `json:"created_at"`
	ExpiresAt        int64          `json:"expires_at"`
	AuthCode         string         `json:"auth_code,omitempty"`
	RefreshToken     string         `json:"refresh_token,omitempty"`
	UserTag          string         `json:"user_tag,omitempty"`
	CBUID            string         `json:"cb_uid,omitempty"`
	CBNickname       string         `json:"cb_nickname,omitempty"`
	Progress         *loginProgress `json:"progress,omitempty"`
}
type oauthPendingStore struct {
	dir  string
	fd   int
	lock *os.File
	key  []byte
}

var pendingStores = struct {
	sync.Mutex
	stores map[string]*oauthPendingStore
}{stores: make(map[string]*oauthPendingStore)}
var authDirCache struct {
	sync.Mutex
	dir string
}

func rememberAuthDir(dir string) {
	if dir = strings.TrimSpace(dir); dir != "" {
		authDirCache.Lock()
		authDirCache.dir = dir
		authDirCache.Unlock()
	}
}
func cachedAuthDir() string {
	authDirCache.Lock()
	defer authDirCache.Unlock()
	return authDirCache.dir
}
func captureAuthDir(raw []byte) {
	var req struct {
		Host struct {
			AuthDir      string
			AuthDirSnake string `json:"auth_dir"`
		}
	}
	if json.Unmarshal(raw, &req) == nil {
		dir := req.Host.AuthDir
		if dir == "" {
			dir = req.Host.AuthDirSnake
		}
		rememberAuthDir(dir)
	}
}
func pendingLoginPath(dir string) string { return filepath.Join(dir, pendingLoginFileName) }
func oauthPendingName(state string) string {
	s := sha256.Sum256([]byte(state))
	return hex.EncodeToString(s[:]) + ".pending"
}
func oauthRandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", errOAuthStorage
	}
	return hex.EncodeToString(b), nil
}
func validOAuthState(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func validOAuthPendingName(n string) bool {
	if len(n) != 72 || !strings.HasSuffix(n, ".pending") {
		return false
	}
	_, err := hex.DecodeString(n[:64])
	return err == nil && strings.ToLower(n) == n
}
func validOAuthPendingRecord(r *pendingLoginRecord) bool {
	if !validOAuthState(r.State) || (r.Flow != "cn" && r.Flow != "intl") || r.CodeVerifier == "" || len(r.CodeVerifier) > 128 {
		return false
	}
	if r.CreatedAt <= 0 || r.CreatedAt > time.Now().Add(time.Minute).Unix() || r.ExpiresAt <= r.CreatedAt || r.ExpiresAt > r.CreatedAt+int64((loginTTL+loginOutcomeTTL)/time.Second) {
		return false
	}
	if p := r.Progress; p != nil {
		if p.Owner != "" && p.Owner != "host" && p.Owner != "self" {
			return false
		}
		if p.Token != nil && (!p.ExchangeStarted || p.Token.Access == "") {
			return false
		}
		if p.Result != nil && (p.Token == nil || p.Result.FileName == "" || filepath.Base(p.Result.FileName) != p.Result.FileName || !json.Valid(p.Result.StorageJSON)) {
			return false
		}
		if p.Saved && (p.Result == nil || p.Owner != "self") {
			return false
		}
	}
	return true
}

// Caller holds pendingStores mutex. Pin at most one production directory;
// tests can close stores explicitly. No cwd/HOME fallback and no symlink paths.
func pendingStore(dir string) (*oauthPendingStore, error) {
	if !filepath.IsAbs(dir) {
		return nil, errOAuthStorage
	}
	dir = filepath.Clean(dir)
	if s := pendingStores.stores[dir]; s != nil {
		return s, nil
	}
	if len(pendingStores.stores) >= 128 {
		return nil, errOAuthStorage
	}
	s, err := openOAuthPendingStore(pendingLoginPath(dir))
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errOAuthStorage
	}
	pendingStores.stores[dir] = s
	return s, nil
}
func persistPendingLogin(rec pendingLoginRecord) error {
	if rec.AuthDir == "" {
		return nil
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = time.Now().Unix()
	}
	pendingStores.Lock()
	defer pendingStores.Unlock()
	s, err := pendingStore(rec.AuthDir)
	if err != nil {
		return err
	}
	return s.write(&rec)
}
func loadPendingLogin(dir string) (pendingLoginRecord, bool) { return loadPendingLoginState(dir, "") }
func loadPendingLoginState(dir, state string) (pendingLoginRecord, bool) {
	pendingStores.Lock()
	defer pendingStores.Unlock()
	if dir == "" {
		return pendingLoginRecord{}, false
	}
	s, err := pendingStore(dir)
	if err != nil {
		return pendingLoginRecord{}, false
	}
	if state != "" {
		if !validOAuthState(state) {
			return pendingLoginRecord{}, false
		}
		r, err := s.read(oauthPendingName(state))
		if err != nil || time.Now().Unix() >= r.ExpiresAt {
			return pendingLoginRecord{}, false
		}
		r.AuthDir = dir
		return *r, true
	}
	records, err := s.records()
	if err != nil || len(records) != 1 {
		return pendingLoginRecord{}, false
	}
	records[0].AuthDir = dir
	return *records[0], true
}

// No state means NO deletion, never an arbitrary other login's record.
func clearPendingLogin(dir string, states ...string) {
	if dir == "" || len(states) != 1 || !validOAuthState(states[0]) {
		return
	}
	pendingStores.Lock()
	defer pendingStores.Unlock()
	s, err := pendingStore(dir)
	if err == nil {
		s.remove(oauthPendingName(states[0]))
	}
}
func stateIsLive(state string) bool {
	if state == "" {
		return false
	}
	if _, ok := loginStates.Load(state); ok {
		return true
	}
	_, ok := intlloginStates.Load(state)
	return ok
}

var restorePendingMu sync.Mutex

func restorePendingLoginState(state string) string {
	restorePendingMu.Lock()
	defer restorePendingMu.Unlock()
	if stateIsLive(state) {
		return state
	}
	if !admitLogin() {
		return ""
	}
	rec, ok := loadPendingLoginState(cachedAuthDir(), state)
	if !ok {
		return ""
	}
	p := rec.Progress
	if p == nil {
		p = &loginProgress{CreatedAt: rec.CreatedAt}
	}
	if rec.Flow == "intl" {
		lc := &intlloginCtx{state: rec.State, cbURL: rec.CbURL, expires: time.Unix(rec.ExpiresAt, 0), loginTraceID: rec.State, codeVerifier: rec.CodeVerifier, codeChallenge: rec.CodeChallenge, deviceID: rec.DeviceID, machineID: rec.MachineID, loginHost: rec.LoginHost, authDir: rec.AuthDir, devicePublicKey: rec.DevicePublicKey, devicePrivateKey: rec.DevicePrivateKey, restored: true, authCode: rec.AuthCode, userTag: rec.UserTag, cbUID: rec.CBUID, cbNickname: rec.CBNickname}
		restoreLoginProgress(&lc.loginProgress, p)
		intlloginStates.LoadOrStore(rec.State, lc)
	} else {
		lc := &loginCtx{variant: rec.Variant, state: rec.State, cbURL: rec.CbURL, expires: time.Unix(rec.ExpiresAt, 0), loginTraceID: rec.State, codeVerifier: rec.CodeVerifier, codeChallenge: rec.CodeChallenge, deviceID: rec.DeviceID, machineID: rec.MachineID, loginHost: rec.LoginHost, authDir: rec.AuthDir, devicePublicKey: rec.DevicePublicKey, devicePrivateKey: rec.DevicePrivateKey, restored: true, authCode: rec.AuthCode, refreshToken: rec.RefreshToken, userTag: rec.UserTag, cbUID: rec.CBUID, cbNickname: rec.CBNickname}
		restoreLoginProgress(&lc.loginProgress, p)
		loginStates.LoadOrStore(rec.State, lc)
	}
	return rec.State
}
func spawnSelfCompleteCN(lc *loginCtx) {
	lc.mu.Lock()
	if lc.selfRunning || lc.Owner == "host" || lc.Saved {
		lc.mu.Unlock()
		return
	}
	lc.selfRunning = true
	grace := loginSelfCompleteGrace
	lc.mu.Unlock()
	go func() {
		defer func() { lc.mu.Lock(); lc.selfRunning = false; lc.mu.Unlock() }()
		selfCompleteCNAfterGrace(lc, grace)
	}()
}
func spawnSelfCompleteIntl(lc *intlloginCtx) {
	lc.mu.Lock()
	if lc.selfRunning || lc.Owner == "host" || lc.Saved {
		lc.mu.Unlock()
		return
	}
	lc.selfRunning = true
	grace := loginSelfCompleteGrace
	lc.mu.Unlock()
	go func() {
		defer func() { lc.mu.Lock(); lc.selfRunning = false; lc.mu.Unlock() }()
		selfCompleteIntlAfterGrace(lc, grace)
	}()
}
func selfCompleteCNAfterGrace(lc *loginCtx, grace time.Duration) {
	time.Sleep(grace)
	if v, ok := loginStates.Load(lc.state); ok && v == lc {
		selfCompleteCN(lc, "callback")
	}
}
func selfCompleteIntlAfterGrace(lc *intlloginCtx, grace time.Duration) {
	time.Sleep(grace)
	if v, ok := intlloginStates.Load(lc.state); ok && v == lc {
		selfCompleteIntl(lc, "callback")
	}
}
func selfCompleteCN(lc *loginCtx, source string) {
	if _, err := advanceLoginCN(lc, true); err != nil {
		recordLoginOutcome(lc.state, false, err.Error())
	}
}
func selfCompleteIntl(lc *intlloginCtx, source string) {
	if _, err := advanceLoginIntl(lc, true); err != nil {
		recordLoginOutcome(lc.state, false, err.Error())
	}
}

type loginOutcome struct {
	ok  bool
	msg string
	at  time.Time
}

var outcomeCache struct {
	sync.Mutex
	m map[string]loginOutcome
}

func recordLoginOutcome(state string, ok bool, msg string) {
	if state == "" {
		return
	}
	outcomeCache.Lock()
	defer outcomeCache.Unlock()
	if outcomeCache.m == nil {
		outcomeCache.m = make(map[string]loginOutcome)
	}
	for k, v := range outcomeCache.m {
		if time.Since(v.at) > loginOutcomeTTL {
			delete(outcomeCache.m, k)
		}
	}
	if len(outcomeCache.m) >= oauthPendingMaxFiles {
		for k := range outcomeCache.m {
			delete(outcomeCache.m, k)
			break
		}
	}
	outcomeCache.m[state] = loginOutcome{ok, msg, time.Now()}
}
func lookupLoginOutcome(state string) (loginOutcome, bool) {
	outcomeCache.Lock()
	defer outcomeCache.Unlock()
	o, ok := outcomeCache.m[state]
	if ok && time.Since(o.at) > loginOutcomeTTL {
		delete(outcomeCache.m, state)
		ok = false
	}
	return o, ok
}

type loginStatusOutcome struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	AgeSec  int64  `json:"age_seconds"`
}

type loginStatusPayload struct {
	State           string `json:"state,omitempty"`
	Stage           string `json:"stage,omitempty"`
	Persistent      bool   `json:"persistent"`
	Retryable       bool   `json:"retryable"`
	RestartRequired bool   `json:"restart_required"`
	Saved           bool   `json:"saved"`
	Owner           string `json:"owner,omitempty"`
	Pending         bool   `json:"pending"`
	Restored        bool   `json:"restored"`
	Variant         string `json:"variant,omitempty"`
	CallbackHP      string `json:"callback_hostport,omitempty"`
	AgeSec          int64  `json:"age_seconds,omitempty"`
	TTLRemaining    int64  `json:"ttl_seconds_remaining,omitempty"`
	ListenerAlive   bool   `json:"listener_alive"`
	// CallbackReceived: the browser redirect already landed (or a paste was
	// accepted) and the token exchange is pending the next poll drain.
	CallbackReceived bool                  `json:"callback_received,omitempty"`
	Outcome          *loginStatusOutcome   `json:"outcome,omitempty"`
	ExchangeFailure  *loginExchangeFailure `json:"exchange_failure,omitempty"`
}

func mgmtJSONResourceResponse(body []byte) pluginapi.ManagementResponse {
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: body}
}

// callbackHostPort reduces a callback URL to its host:port - the only part
// the user needs to recognize the browser's "cannot connect" error page.
func callbackHostPort(cbURL string) string {
	cbURL = strings.TrimSpace(cbURL)
	if cbURL == "" {
		return ""
	}
	if u, err := url.Parse(cbURL); err == nil && u.Host != "" {
		return u.Host
	}
	return ""
}

// latestLoginOutcome returns the most recent recorded login outcome across
// states (the cache is keyed by state; the status line is stateless).
func latestLoginOutcome() (loginOutcome, bool) {
	outcomeCache.Lock()
	defer outcomeCache.Unlock()
	var best loginOutcome
	found := false
	for _, o := range outcomeCache.m {
		if time.Since(o.at) <= loginOutcomeTTL && (!found || o.at.After(best.at)) {
			best, found = o, true
		}
	}
	return best, found
}

// pendingSnapshot is the display-relevant slice of a live or restored login.
type pendingSnapshot struct {
	state            string
	variant          string
	cbURL            string
	expires          time.Time
	restored         bool
	listenerAlive    bool
	callbackReceived bool
}

// snapshotPending picks the pending login with the latest expiry across both
// flow maps (cn/solo share loginStates; intl has its own). Expired entries
// the janitor has not reaped yet are ignored.
func snapshotPending() (pendingSnapshot, bool) {
	now := time.Now()
	var best pendingSnapshot
	found := false
	consider := func(state, variant, cbURL string, expires time.Time, restored, listenerAlive, cbRecv bool) {
		if !now.Before(expires) {
			return
		}
		if !found || expires.After(best.expires) {
			best = pendingSnapshot{
				state: state, variant: variant, cbURL: cbURL, expires: expires,
				restored: restored, listenerAlive: listenerAlive, callbackReceived: cbRecv,
			}
			found = true
		}
	}
	loginStates.Range(func(_, v any) bool {
		if lc, ok := v.(*loginCtx); ok {
			lc.mu.Lock()
			got := lc.authCode != "" || lc.refreshToken != "" || lc.err != nil
			if !lc.Saved && lc.Owner != "host" {
				consider(lc.state, lc.variant, lc.cbURL, lc.expires, lc.restored, lc.listener != nil, got)
			}
			lc.mu.Unlock()
		}
		return true
	})
	intlloginStates.Range(func(_, v any) bool {
		if lc, ok := v.(*intlloginCtx); ok {
			lc.mu.Lock()
			got := lc.authCode != "" || lc.err != nil
			if !lc.Saved && lc.Owner != "host" {
				consider(lc.state, "intl", lc.cbURL, lc.expires, lc.restored, lc.listener != nil, got)
			}
			lc.mu.Unlock()
		}
		return true
	})
	return best, found
}

// handleLoginStatusResource answers the panel's status poll. Priority: the
// in-memory pending login (live listener or restored-into-memory) wins; the
// disk record is reported only when nothing is in memory (post-restart, the
// paste can still complete it). The newest outcome is always attached.
func handleLoginStatusResource() []byte {
	now := time.Now()
	payload := loginStatusPayload{}
	if snap, ok := snapshotPending(); ok {
		payload = oauthLoginStatus(snap.state)
		payload.Pending = !payload.RestartRequired
		payload.Restored = snap.restored
		payload.Variant = snap.variant
		payload.CallbackHP = callbackHostPort(snap.cbURL)
		payload.AgeSec = int64((loginTTL - time.Until(snap.expires)).Seconds())
		payload.TTLRemaining = int64(time.Until(snap.expires).Seconds())
		payload.ListenerAlive = snap.listenerAlive
		payload.CallbackReceived = snap.callbackReceived
	} else if dir := cachedAuthDir(); dir != "" {
		// loadPendingLogin also sweeps an expired record (cleanup side effect).
		if rec, ok := loadPendingLogin(dir); ok {
			payload = oauthLoginStatus(rec.State)
			payload.Pending = !payload.RestartRequired && (rec.Progress == nil || (!rec.Progress.Saved && rec.Progress.Owner != "host"))
			payload.Restored = true // not in memory -> only the disk record serves it
			payload.Variant = rec.Variant
			payload.CallbackHP = callbackHostPort(rec.CbURL)
			payload.AgeSec = now.Unix() - rec.CreatedAt
			payload.TTLRemaining = rec.ExpiresAt - now.Unix()
		}
	}
	if o, ok := latestLoginOutcome(); ok {
		payload.Outcome = &loginStatusOutcome{OK: o.ok, Message: o.msg, AgeSec: int64(time.Since(o.at).Seconds())}
	}
	// This legacy endpoint is public. Never disclose the capability-like state
	// there; authenticated management status includes only its requested state.
	payload.State = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"pending":false}`)
	}
	return raw
}

func restoreLoginProgress(dst, src *loginProgress) {
	dst.CreatedAt = src.CreatedAt
	dst.Owner = src.Owner
	dst.ExchangeStarted = src.ExchangeStarted
	dst.Token = src.Token
	dst.Result = src.Result
	dst.Saved = src.Saved
	dst.Failure = src.Failure
	dst.ExchangeFailure = src.ExchangeFailure.sanitized()
	// A persisted started exchange with no checkpointed token cannot still be
	// running after restore. Do not retry it or present it as waiting forever.
	if dst.ExchangeStarted && dst.Token == nil && dst.Result == nil && dst.ExchangeFailure == nil {
		dst.ExchangeFailure = &loginExchangeFailure{Category: "outcome_uncertain"}
	}
}
