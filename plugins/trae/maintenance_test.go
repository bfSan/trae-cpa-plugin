package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type maintenanceFixture struct {
	mu    sync.Mutex
	doc   []byte
	files []pluginapi.HostAuthFileEntry
	saves int
}

func mockMaintenance(t *testing.T) *maintenanceFixture {
	t.Helper()
	oldList, oldRaw, oldSave := maintenanceAuthList, maintenanceAuthRaw, maintenanceAuthSave
	oldStatus, oldClaim, oldRefresh := checkinStatusCall, checkinClaimCall, checkinRefreshCall
	authDirCache.Lock()
	oldDir := authDirCache.dir
	authDirCache.dir = ""
	authDirCache.Unlock()
	f := &maintenanceFixture{doc: []byte(`{"type":"trae","provider":"trae","disabled":false,"label":"custom","unknown":{"kept":true},"auth":{"accessToken":"old","refreshToken":"refresh-old","expiresAt":9999999999,"variant":"cn","deviceId":"1234567890123456","devicePublicKey":"public","devicePrivateKey":"private"},"account":{"uid":"uid","nickname":"nick"}}`), files: []pluginapi.HostAuthFileEntry{{AuthIndex: "idx", ID: "id", Name: "trae-uid.json"}}}
	maintenanceAuthList = func() ([]pluginapi.HostAuthFileEntry, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]pluginapi.HostAuthFileEntry(nil), f.files...), nil
	}
	maintenanceAuthRaw = func(index string) (json.RawMessage, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if index != "idx" {
			return nil, errors.New("missing")
		}
		return append([]byte(nil), f.doc...), nil
	}
	maintenanceAuthSave = func(name string, raw []byte) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if name != "trae-uid.json" {
			return errors.New("bad name")
		}
		f.saves++
		f.doc = append([]byte(nil), raw...)
		return nil
	}
	checkinRefreshCall = func(*authSnapshot, time.Duration) (bool, error) { return false, nil }
	checkinAttempts.Delete("idx")
	accountCache.Delete("idx")
	t.Cleanup(func() {
		maintenanceAuthList, maintenanceAuthRaw, maintenanceAuthSave = oldList, oldRaw, oldSave
		checkinStatusCall, checkinClaimCall, checkinRefreshCall = oldStatus, oldClaim, oldRefresh
		authDirCache.Lock()
		authDirCache.dir = oldDir
		authDirCache.Unlock()
		checkinAttempts.Delete("idx")
		accountCache.Delete("idx")
	})
	return f
}
func TestMaintenanceRefreshPreservesDisabledAndMetadata(t *testing.T) {
	f := mockMaintenance(t)
	s, err := freshAuthSnapshot("idx")
	if err != nil {
		t.Fatal(err)
	}
	if s.Auth.DevicePrivateKey != "private" {
		t.Fatal("device keys lost during host snapshot parsing")
	}
	var doc map[string]any
	_ = json.Unmarshal(f.doc, &doc)
	doc["disabled"] = true
	doc["label"] = "new-label"
	f.doc, _ = json.Marshal(doc)
	s.Auth.AccessToken = "new"
	s.Auth.RefreshToken = "refresh-new"
	if err := saveRefreshedAuth(s, s.Auth); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(f.doc, &doc)
	if doc["disabled"] != true || doc["label"] != "new-label" || doc["unknown"] == nil {
		t.Fatalf("refresh lost current metadata: %s", f.doc)
	}
}
func TestMaintenanceRefreshCASAndDeletedAccount(t *testing.T) {
	f := mockMaintenance(t)
	s, _ := freshAuthSnapshot("idx")
	var doc map[string]any
	_ = json.Unmarshal(f.doc, &doc)
	doc["auth"].(map[string]any)["refreshToken"] = "another-flight"
	f.doc, _ = json.Marshal(doc)
	s.Auth.AccessToken = "stale"
	if err := saveRefreshedAuth(s, s.Auth); err == nil {
		t.Fatal("stale refresh overwrote newer token")
	}
	if f.saves != 0 {
		t.Fatal("stale refresh must not save")
	}
	f.files = nil
	if err := saveRefreshedAuth(s, s.Auth); err == nil {
		t.Fatal("deleted account was resurrected")
	}
	if f.saves != 0 {
		t.Fatal("deleted refresh must not save")
	}
}
func TestMaintenancePhysicalDeletionAndSymlink(t *testing.T) {
	f := mockMaintenance(t)
	dir := t.TempDir()
	authDirCache.Lock()
	authDirCache.dir = dir
	authDirCache.Unlock()
	if _, err := freshAuthSnapshot("idx"); err == nil {
		t.Fatal("stale host entry without physical file accepted")
	}
	other := filepath.Join(t.TempDir(), "other.json")
	if err := os.WriteFile(other, f.doc, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(dir, "trae-uid.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := freshAuthSnapshot("idx"); err == nil {
		t.Fatal("symlink credential accepted")
	}
}
func TestMaintenanceCheckinUnconfirmedOnlyVerifies(t *testing.T) {
	mockMaintenance(t)
	var claims, statuses int
	checkinStatusCall = func(a *auth.Auth, did string) (*upstream.CheckinStatusResult, error) {
		statuses++
		if did != a.DeviceID {
			t.Fatal("check-in device changed")
		}
		if statuses == 2 {
			return nil, errors.New("lost verification")
		}
		return &upstream.CheckinStatusResult{Enable: true, Credits: 150}, nil
	}
	checkinClaimCall = func(a *auth.Auth, did string) (*upstream.CheckinClaimResult, error) {
		claims++
		if did != a.DeviceID {
			t.Fatal("claim changed identity")
		}
		return &upstream.CheckinClaimResult{Code: 0}, nil
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, checkinDayLocation)
	first := checkinAccountLocked("idx", now)
	if first["confirmed"] != false || first["claim_unconfirmed"] != true || first["verify_error"] == nil {
		t.Fatalf("ambiguous claim reported success: %+v", first)
	}
	second := checkinAccountLocked("idx", now.Add(3*time.Minute))
	if claims != 1 || second["confirmed"] != false || second["claim_unconfirmed"] != true {
		t.Fatalf("uncertain claim replayed: claims=%d result=%+v", claims, second)
	}
}
func TestMaintenanceCheckinTransportAmbiguityNeverReclaims(t *testing.T) {
	mockMaintenance(t)
	claims := 0
	checkinStatusCall = func(*auth.Auth, string) (*upstream.CheckinStatusResult, error) {
		return &upstream.CheckinStatusResult{Enable: true}, nil
	}
	checkinClaimCall = func(*auth.Auth, string) (*upstream.CheckinClaimResult, error) {
		claims++
		return nil, errors.New("response lost after write")
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, checkinDayLocation)
	checkinAccountLocked("idx", now)
	checkinAccountLocked("idx", now.Add(3*time.Minute))
	if claims != 1 {
		t.Fatalf("ambiguous transport claim replayed %d times", claims)
	}
}
func TestMaintenanceCheckinConfirmedAndRewardNotBalance(t *testing.T) {
	mockMaintenance(t)
	statuses := 0
	checkinStatusCall = func(*auth.Auth, string) (*upstream.CheckinStatusResult, error) {
		statuses++
		return &upstream.CheckinStatusResult{Enable: true, CheckedIn: statuses > 1, Credits: 150}, nil
	}
	checkinClaimCall = func(*auth.Auth, string) (*upstream.CheckinClaimResult, error) {
		return &upstream.CheckinClaimResult{Code: 0}, nil
	}
	result := checkinAccount("idx")
	if result["confirmed"] != true {
		t.Fatalf("expected confirmed: %+v", result)
	}
	value, _ := accountCache.Load("idx")
	entry := value.(*accountCacheEntry)
	if entry.credits != -1 || entry.usageFilled {
		t.Fatalf("reward became balance: %+v", entry)
	}
}
func TestMaintenanceCheckinSameAccountLockAndBudget(t *testing.T) {
	mockMaintenance(t)
	var active, maxActive, calls atomic.Int32
	checkinStatusCall = func(*auth.Auth, string) (*upstream.CheckinStatusResult, error) {
		n := active.Add(1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		calls.Add(1)
		active.Add(-1)
		return nil, errors.New("temporary")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); checkinAccount("idx") }()
	}
	wg.Wait()
	if maxActive.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("manual/auto lock/backoff violated: active=%d calls=%d", maxActive.Load(), calls.Load())
	}
	current := time.Now().In(checkinDayLocation)
	now := time.Date(current.Year(), current.Month(), current.Day(), 12, 0, 0, 0, checkinDayLocation)
	value, _ := checkinAttempts.Load("idx")
	st := *value.(*checkinAttemptState)
	st.next = now
	checkinAttempts.Store("idx", &st)
	for i := 1; i < 12; i++ {
		checkinAccountLocked("idx", now.Add(time.Duration(i)*20*time.Minute))
	}
	if calls.Load() != maxDailyCheckinAttempts {
		t.Fatalf("per-account daily budget exceeded: calls=%d", calls.Load())
	}
}
func TestMaintenanceCheckinDisabledNeverCallsUpstream(t *testing.T) {
	f := mockMaintenance(t)
	f.files[0].Disabled = true
	checkinStatusCall = func(*auth.Auth, string) (*upstream.CheckinStatusResult, error) {
		t.Fatal("disabled account contacted upstream")
		return nil, nil
	}
	result := checkinAccount("idx")
	if result["confirmed"] != false || result["error"] == nil {
		t.Fatalf("disabled response: %+v", result)
	}
}
func TestMaintenanceCacheFieldCAS(t *testing.T) {
	const index = "cache-cas"
	defer accountCache.Delete(index)
	now := time.Now()
	newer := now.Add(time.Second)
	updateUsageCache(index, newer, upstream.UsageSummary{RemainKnown: true, Remain: 42}, "fresh", &bindStatus{Known: true, DeviceMatch: true}, -1, false, false)
	updateCheckinCache(index, now, false, &upstream.CheckinStatusResult{Credits: 150, CheckedIn: true})
	value, _ := accountCache.Load(index)
	entry := value.(*accountCacheEntry)
	if entry.plan != "fresh" || entry.bind == nil || !entry.bind.DeviceMatch || entry.checkin == nil {
		t.Fatalf("field merge lost data: %+v", entry)
	}
	updateCheckinCache(index, newer.Add(time.Second), true, &upstream.CheckinStatusResult{Credits: 150, CheckedIn: true})
	updateUsageCache(index, now, upstream.UsageSummary{RemainKnown: true, Remain: 0}, "stale", nil, -1, false, false)
	value, _ = accountCache.Load(index)
	entry = value.(*accountCacheEntry)
	if entry.usageFilled || entry.credits != -1 || entry.bind == nil {
		t.Fatalf("stale usage resurrected after claim: %+v", entry)
	}
}
func TestMaintenanceSaveLoginPreservesManualIntent(t *testing.T) {
	f := mockMaintenance(t)
	dir := t.TempDir()
	name := "trae-uid.json"
	var doc map[string]any
	_ = json.Unmarshal(f.doc, &doc)
	doc["disabled"] = true
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	result := &pluginapi.AuthData{FileName: name, StorageJSON: []byte(`{"disabled":false,"auth":{"accessToken":"fresh"},"account":{"uid":"uid"}}`)}
	if err := safeSaveLoginResult(dir, result); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(result.StorageJSON, &doc)
	fields := doc["auth"].(map[string]any)
	if fields["accessToken"] != "fresh" || fields["devicePrivateKey"] != "private" {
		t.Fatalf("login merge lost new tokens or old nested fields: %s", result.StorageJSON)
	}
	if doc["disabled"] != true || doc["unknown"] == nil || doc["label"] != "custom" {
		t.Fatalf("re-login erased manual intent: %s", result.StorageJSON)
	}
}

func TestMaintenancePendingClaimSurvivesRestart(t *testing.T) {
	mockMaintenance(t)
	claims := 0
	checkinStatusCall = func(*auth.Auth, string) (*upstream.CheckinStatusResult, error) {
		return &upstream.CheckinStatusResult{Enable: true, Credits: 150}, nil
	}
	checkinClaimCall = func(*auth.Auth, string) (*upstream.CheckinClaimResult, error) {
		claims++
		return &upstream.CheckinClaimResult{Code: 0}, nil
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, checkinDayLocation)
	checkinAccountLocked("idx", now)
	checkinAttempts.Delete("idx") // simulate restart: physical checkpoint is retained
	result := checkinAccountLocked("idx", now.Add(3*time.Minute))
	if claims != 1 || result["claim_unconfirmed"] != true || result["confirmed"] != false {
		t.Fatalf("restart replayed pending claim: count=%d result=%+v", claims, result)
	}
}
func TestMaintenanceCheckpointFailurePreventsClaim(t *testing.T) {
	mockMaintenance(t)
	maintenanceAuthSave = func(string, []byte) error { return errors.New("disk full") }
	checkinStatusCall = func(*auth.Auth, string) (*upstream.CheckinStatusResult, error) {
		t.Fatal("upstream called without durable budget")
		return nil, nil
	}
	result := checkinAccount("idx")
	if result["confirmed"] != false || result["error"] == nil {
		t.Fatalf("checkpoint failure hidden: %+v", result)
	}
}
func TestMaintenanceRefreshSaveFailureStopsExecutor(t *testing.T) {
	mockMaintenance(t)
	previous := maintenanceRefreshCN
	t.Cleanup(func() { maintenanceRefreshCN = previous })
	maintenanceRefreshCN = func(a *auth.Auth, _ time.Duration) (bool, error) {
		a.AccessToken = "rotated"
		a.RefreshToken = "rotated-refresh"
		return true, nil
	}
	maintenanceAuthSave = func(string, []byte) error { return errors.New("save failed") }
	a, err := refreshExecutorAuth(pluginapi.ExecutorRequest{AuthID: "id"}, &auth.Auth{AccessToken: "fallback"})
	if err == nil || a != nil {
		t.Fatalf("unpersisted rotation may reach chat: auth=%v err=%v", a, err)
	}
}
func TestMaintenanceAutoOffDoesNotRun(t *testing.T) {
	mockMaintenance(t)
	oldAuto, oldKeep := maintenanceEnabled()
	t.Cleanup(func() { configureMaintenance(oldAuto, oldKeep) })
	configureMaintenance(false, false)
	checkinStatusCall = func(*auth.Auth, string) (*upstream.CheckinStatusResult, error) {
		t.Fatal("disabled auto contacted upstream")
		return nil, nil
	}
	checkinAccountAutomatic("idx")
}

func TestMaintenanceScheduleNoStartupCatchup(t *testing.T) {
	after := time.Date(2026, 10, 22, 11, 0, 0, 0, checkinDayLocation)
	next := nextMaintenanceCheckin(after)
	if next.Day() != 23 || next.Hour() != 9 {
		t.Fatalf("startup after 09 must wait next day: %v", next)
	}
	before := time.Date(2026, 10, 22, 8, 59, 0, 0, checkinDayLocation)
	if next := nextMaintenanceCheckin(before); next.Day() != 22 || next.Hour() != 9 {
		t.Fatalf("09 firing lost: %v", next)
	}
	if maintenanceCheckinDue("never-started", after, false) {
		t.Fatal("new account got restart catchup")
	}
}
func TestMaintenancePrepareLoginDoesNotWrite(t *testing.T) {
	f := mockMaintenance(t)
	dir := t.TempDir()
	name := "trae-uid.json"
	if err := os.WriteFile(filepath.Join(dir, name), f.doc, 0600); err != nil {
		t.Fatal(err)
	}
	result := &pluginapi.AuthData{FileName: name, StorageJSON: []byte(`{"auth":{"accessToken":"new-token","refreshToken":"new-refresh"},"account":{"uid":"uid","nickname":"new-name"}}`)}
	if err := prepareLoginResult(dir, result); err != nil {
		t.Fatal(err)
	}
	var merged map[string]any
	_ = json.Unmarshal(result.StorageJSON, &merged)
	fields := merged["auth"].(map[string]any)
	if fields["accessToken"] != "new-token" || fields["refreshToken"] != "new-refresh" || fields["devicePrivateKey"] != "private" || merged["account"].(map[string]any)["nickname"] != "new-name" {
		t.Fatalf("new login fields lost: %s", result.StorageJSON)
	}
	disk, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != string(f.doc) {
		t.Fatal("prepareLoginResult wrote credential before host delivery")
	}
}

func TestMaintenanceExplicit9074RetriesWithinAccountBudget(t *testing.T) {
	mockMaintenance(t)
	claims := 0
	checkinStatusCall = func(*auth.Auth, string) (*upstream.CheckinStatusResult, error) {
		return &upstream.CheckinStatusResult{Enable: true}, nil
	}
	checkinClaimCall = func(*auth.Auth, string) (*upstream.CheckinClaimResult, error) {
		claims++
		return nil, &upstream.Error{BizCode: 9074}
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, checkinDayLocation)
	first := checkinAccountLocked("idx", now)
	if first["claim_unconfirmed"] == true {
		t.Fatal("explicit rejection must not become an uncertain receipt")
	}
	for i := 1; i < 8; i++ {
		checkinAccountLocked("idx", now.Add(time.Duration(i)*20*time.Minute))
	}
	if claims != maxDailyCheckinAttempts {
		t.Fatalf("9074 retries bypassed account budget: %d", claims)
	}
}
