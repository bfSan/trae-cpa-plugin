package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func mockModelRefresh(t *testing.T) (*maintenanceFixture, *atomic.Int32) {
	t.Helper()
	f := mockMaintenance(t)
	oldFetch, oldNow := fetchModelsFn, modelRefreshNow
	calls := &atomic.Int32{}
	fetchModelsFn = func(*auth.Auth) ([]upstream.ModelInfo, error) {
		calls.Add(1)
		return []upstream.ModelInfo{{ID: "daily-model", Name: "Daily", ContextWindow: 32768}}, nil
	}
	modelRefreshNow = func() time.Time { return time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { fetchModelsFn, modelRefreshNow = oldFetch, oldNow })
	return f, calls
}
func TestModelRefreshDailyAndRestartCheckpoint(t *testing.T) {
	f, calls := mockModelRefresh(t)
	first := refreshAccountModels("idx", false)
	if !first.Updated || calls.Load() != 1 {
		t.Fatalf("first=%+v calls=%d", first, calls.Load())
	}
	original := modelRefreshNow()
	modelRefreshNow = func() time.Time { return original.Add(24*time.Hour - time.Second) }
	if got := refreshAccountModels("idx", false); got.Updated || calls.Load() != 1 {
		t.Fatalf("early refresh: %+v", got)
	}
	// There is no in-memory last-attempt cache: repeat simulates restart.
	modelRefreshNow = func() time.Time { return original.Add(24 * time.Hour) }
	fetchModelsFn = func(*auth.Auth) ([]upstream.ModelInfo, error) {
		calls.Add(1)
		return nil, errors.New("catalog unavailable")
	}
	got := refreshAccountModels("idx", false)
	if got.Updated || got.Error == "" || len(got.Models) != 1 {
		t.Fatalf("failed refresh lost old catalog: %+v", got)
	}
	modelRefreshNow = func() time.Time { return original.Add(25 * time.Hour) }
	refreshAccountModels("idx", false)
	if calls.Load() != 2 {
		t.Fatalf("failed attempt retried before 24h: %d", calls.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var doc map[string]any
	_ = json.Unmarshal(f.doc, &doc)
	if doc["unknown"] == nil || extractTraeAccessToken(f.doc) != "old" {
		t.Fatal("metadata update modified credential")
	}
	if s := readModelRefreshState(f.doc); s.LastAttemptAt == "" || s.Status != "failed" {
		t.Fatalf("missing durable failed attempt: %+v", s)
	}
}
func TestModelRefreshSkipsDisabledDeletedExpired(t *testing.T) {
	for _, kind := range []string{"disabled", "deleted", "expired"} {
		t.Run(kind, func(t *testing.T) {
			f, calls := mockModelRefresh(t)
			if kind == "disabled" {
				f.files[0].Disabled = true
			}
			if kind == "deleted" {
				f.files = nil
			}
			if kind == "expired" {
				var doc map[string]any
				_ = json.Unmarshal(f.doc, &doc)
				doc["auth"].(map[string]any)["expiresAt"] = 1
				f.doc, _ = json.Marshal(doc)
			}
			got := refreshAccountModels("idx", false)
			if got.Updated || got.Error == "" || calls.Load() != 0 {
				t.Fatalf("%s: %+v calls=%d", kind, got, calls.Load())
			}
		})
	}
}
func TestModelRefreshConcurrentSingleflightAndWatcherReentry(t *testing.T) {
	f, calls := mockModelRefresh(t)
	entered, release := make(chan struct{}), make(chan struct{})
	fetchModelsFn = func(*auth.Auth) ([]upstream.ModelInfo, error) {
		calls.Add(1)
		close(entered)
		<-release
		return []upstream.ModelInfo{{ID: "fresh"}}, nil
	}
	originalSave := maintenanceAuthSave
	maintenanceAuthSave = func(name string, raw []byte) error {
		if err := originalSave(name, raw); err != nil {
			return err
		}
		// Exactly the synchronous HostSave -> model.for_auth recursion.
		a, _ := parseStoredAuth(raw)
		_ = accountModelsForStorage(a, raw, false)
		return nil
	}
	var wg sync.WaitGroup
	results := make(chan modelRefreshResult, 12)
	wg.Add(1)
	go func() { defer wg.Done(); results <- refreshAccountModels("idx", false) }()
	<-entered
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- refreshAccountModels("idx", false) }()
	}
	// Registration is nonblocking while a refresh is in flight.
	f.mu.Lock()
	raw := append([]byte(nil), f.doc...)
	f.mu.Unlock()
	a, _ := parseStoredAuth(raw)
	_ = accountModelsForStorage(a, raw, false)
	close(release)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("model refresh deadlocked")
	}
	close(results)
	for r := range results {
		if r.Error != "" {
			t.Fatalf("refresh failed: %+v", r)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("singleflight calls=%d", calls.Load())
	}
}
func TestModelRefreshTokenSaveWatcherDoesNotDeadlock(t *testing.T) {
	f, calls := mockModelRefresh(t)
	done := make(chan struct{})
	go func() {
		unlock := accountOperationLock("idx")
		defer unlock()
		a, _ := parseStoredAuth(f.doc)
		_ = accountModelsForStorage(a, f.doc, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("credential-save watcher deadlocked")
	}
	if calls.Load() != 0 {
		t.Fatal("watcher called model API under credential lock")
	}
}
func TestModelRefreshDiscardsChangedCredential(t *testing.T) {
	for _, kind := range []string{"token", "refresh-token", "realm", "disabled", "deleted"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := mockModelRefresh(t)
			fetchModelsFn = func(*auth.Auth) ([]upstream.ModelInfo, error) {
				f.mu.Lock()
				defer f.mu.Unlock()
				var doc map[string]any
				_ = json.Unmarshal(f.doc, &doc)
				fields := doc["auth"].(map[string]any)
				switch kind {
				case "token":
					fields["accessToken"] = "rotated"
				case "refresh-token":
					fields["refreshToken"] = "rotated"
				case "realm":
					fields["variant"] = "solo"
				case "disabled":
					doc["disabled"] = true
				case "deleted":
					f.files = nil
				}
				f.doc, _ = json.Marshal(doc)
				return []upstream.ModelInfo{{ID: "stale-flight"}}, nil
			}
			got := refreshAccountModels("idx", true)
			if got.Updated || got.Status != "superseded" {
				t.Fatalf("stale result: %+v", got)
			}
			if readModelCacheDoc(f.doc) != nil {
				t.Fatal("stale flight committed model cache")
			}
		})
	}
}
func TestHostAuthRefreshIncludesModelsAndPreservesTokenOnModelFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "model-failure"}[fail], func(t *testing.T) {
			f, calls := mockModelRefresh(t)
			oldRefresh := maintenanceRefreshCN
			defer func() { maintenanceRefreshCN = oldRefresh }()
			tokenCalls := 0
			maintenanceRefreshCN = func(a *auth.Auth, _ time.Duration) (bool, error) {
				tokenCalls++
				a.AccessToken = "rotated"
				a.RefreshToken = "rotated-refresh"
				return true, nil
			}
			if fail {
				fetchModelsFn = func(*auth.Auth) ([]upstream.ModelInfo, error) { calls.Add(1); return nil, errors.New("model down") }
			}
			req, _ := json.Marshal(pluginapi.AuthRefreshRequest{AuthID: "idx", StorageJSON: f.doc})
			output, err := refreshAuthRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Result struct {
					Auth         pluginapi.AuthData
					ModelRefresh modelRefreshResult `json:"model_refresh"`
					Error        string             `json:"model_refresh_error"`
				} `json:"result"`
			}
			_ = json.Unmarshal(output, &response)
			// okEnvelope is intentionally inspected below independent of envelope case.
			var envelope map[string]json.RawMessage
			_ = json.Unmarshal(output, &envelope)
			payload := envelope["result"]
			if len(payload) == 0 {
				payload = envelope["data"]
			}
			if len(payload) == 0 {
				payload = output
			}
			var result struct {
				Auth         pluginapi.AuthData
				ModelRefresh modelRefreshResult `json:"model_refresh"`
				Error        string             `json:"model_refresh_error"`
			}
			_ = json.Unmarshal(payload, &result)
			if tokenCalls != 1 || calls.Load() != 1 || extractTraeAccessToken(f.doc) != "rotated" {
				t.Fatalf("token/model counts: %d/%d", tokenCalls, calls.Load())
			}
			if extractTraeAccessToken(result.Auth.StorageJSON) != "rotated" {
				t.Fatalf("returned old auth: %s", output)
			}
			if fail {
				if result.Error == "" || result.ModelRefresh.Updated {
					t.Fatalf("failed model response: %s", output)
				}
			} else if !result.ModelRefresh.Updated || readModelCacheDoc(result.Auth.StorageJSON) == nil {
				t.Fatalf("missing returned cache: %s", output)
			}
		})
	}
}

func TestManualRefreshModelsAfterTokenRotation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			f, calls := mockModelRefresh(t)
			oldRefresh, oldCredits := maintenanceRefreshCN, modelRefreshCreditsFn
			defer func() { maintenanceRefreshCN, modelRefreshCreditsFn = oldRefresh, oldCredits }()
			tokenCalls := 0
			maintenanceRefreshCN = func(a *auth.Auth, _ time.Duration) (bool, error) {
				tokenCalls++
				a.AccessToken = "manual-token"
				a.RefreshToken = "manual-refresh"
				return true, nil
			}
			modelRefreshCreditsFn = func(pluginapi.HostAuthFileEntry, *storedAuth, *auth.Auth) map[string]any {
				return map[string]any{"credits": 123}
			}
			fetchModelsFn = func(a *auth.Auth) ([]upstream.ModelInfo, error) {
				calls.Add(1)
				if a.AccessToken != "manual-token" {
					t.Errorf("discovery used old token")
				}
				if fail {
					return nil, errors.New("catalog down")
				}
				return []upstream.ModelInfo{{ID: "manual-model"}}, nil
			}
			got := refreshAccount("idx", true)
			r, ok := got["model_refresh"].(modelRefreshResult)
			if !ok || got["refreshed"] != true || tokenCalls != 1 || calls.Load() != 1 || extractTraeAccessToken(f.doc) != "manual-token" {
				t.Fatalf("manual result: %+v", got)
			}
			if fail {
				if r.Updated || got["model_refresh_error"] == nil || got["error"] != nil {
					t.Fatalf("model failure corrupted token success: %+v", got)
				}
			} else if !r.Updated {
				t.Fatalf("model not updated: %+v", r)
			}
		})
	}
}
func TestDailyModelsIndependentOfMaintenanceSwitches(t *testing.T) {
	_, calls := mockModelRefresh(t)
	oldAuto, oldKeep := maintenanceEnabled()
	defer configureMaintenance(oldAuto, oldKeep)
	configureMaintenance(false, false)
	refreshModelCatalogsDaily(context.Background(), modelRefreshNow())
	refreshModelCatalogsDaily(context.Background(), modelRefreshNow().Add(time.Minute))
	if calls.Load() != 1 {
		t.Fatalf("daily refresh disabled or retried: %d", calls.Load())
	}
}
func TestModelRefreshSchemaMigrationAndPartialCapability(t *testing.T) {
	f, _ := mockModelRefresh(t)
	f.doc, _ = mergeModelCacheIntoDoc(f.doc, &persistedModelCache{Realm: "cn", FetchedAt: modelRefreshNow().Format(time.RFC3339), Models: variantTestModels("legacy")})
	fetchModelsFn = func(*auth.Auth) ([]upstream.ModelInfo, error) {
		return []upstream.ModelInfo{{ID: "partial", Facts: upstream.ModelFacts{CapabilityStatus: "partial"}}}, nil
	}
	result := refreshAccountModels("idx", false)
	if !result.Updated || result.Status != "partial" || result.Error == "" {
		t.Fatalf("partial refresh: %+v", result)
	}
	if c := readModelCacheDoc(f.doc); c == nil || c.SchemaVersion != modelCacheSchemaVersion || c.Models[0].ID != "partial" {
		t.Fatalf("schema migration failed: %s", f.doc)
	}
}

func TestHostRefreshDuringOldModelFlightUsesRotatedToken(t *testing.T) {
	f, calls := mockModelRefresh(t)
	oldRefresh := maintenanceRefreshCN
	defer func() { maintenanceRefreshCN = oldRefresh }()
	entered, release, rotated := make(chan struct{}), make(chan struct{}), make(chan struct{})
	fetchModelsFn = func(a *auth.Auth) ([]upstream.ModelInfo, error) {
		calls.Add(1)
		if a.AccessToken == "old" {
			close(entered)
			<-release
			return []upstream.ModelInfo{{ID: "obsolete"}}, nil
		}
		return []upstream.ModelInfo{{ID: "new-token-model"}}, nil
	}
	maintenanceRefreshCN = func(a *auth.Auth, _ time.Duration) (bool, error) {
		a.AccessToken = "rotated"
		a.RefreshToken = "rotated-refresh"
		close(rotated)
		return true, nil
	}
	oldDone := make(chan modelRefreshResult, 1)
	go func() { oldDone <- refreshAccountModels("idx", false) }()
	<-entered
	f.mu.Lock()
	raw := append([]byte(nil), f.doc...)
	f.mu.Unlock()
	req, _ := json.Marshal(pluginapi.AuthRefreshRequest{AuthID: "idx", StorageJSON: raw})
	hostDone := make(chan error, 1)
	go func() { _, err := refreshAuthRequest(req); hostDone <- err }()
	<-rotated
	// Ensure the new token has crossed the save boundary before old discovery
	// returns. No network or real RefreshToken runs in this test.
	unlock := accountOperationLock("idx")
	unlock()
	close(release)
	if r := <-oldDone; r.Updated {
		t.Fatal("old model flight committed after token rotation")
	}
	select {
	case err := <-hostDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("auth/model interlock deadlocked")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := readModelCacheDoc(f.doc)
	if calls.Load() != 2 || c == nil || c.Models[0].ID != "new-token-model" {
		t.Fatalf("new token catalog missing: calls=%d cache=%+v", calls.Load(), c)
	}
}

func TestModelRefreshFutureTimestampAndCancellation(t *testing.T) {
	f, calls := mockModelRefresh(t)
	now := modelRefreshNow()
	for _, future := range []time.Duration{4 * time.Minute, 6 * time.Minute, 365 * 24 * time.Hour} {
		raw, _ := mergeModelRefreshState(f.doc, modelRefreshState{Realm: "cn", SchemaVersion: modelCacheSchemaVersion, LastAttemptAt: now.Add(future).Format(time.RFC3339Nano)})
		if due := modelRefreshDue(raw, "cn", now); due != (future > 5*time.Minute) {
			t.Fatalf("future %s due=%v", future, due)
		}
	}
	raw, _ := mergeModelCacheIntoDoc(f.doc, &persistedModelCache{Realm: "cn", SchemaVersion: modelCacheSchemaVersion, FetchedAt: now.Add(6 * time.Minute).Format(time.RFC3339Nano), Models: variantTestModels("future")})
	if !modelRefreshDue(raw, "cn", now) {
		t.Fatal("future snapshot blocks refresh")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	refreshModelCatalogsDaily(ctx, now)
	if calls.Load() != 0 {
		t.Fatal("cancelled daily scan sent request")
	}
}
func TestModelRefreshRetryBudgetReturnsCurrentCache(t *testing.T) {
	f, calls := mockModelRefresh(t)
	f.doc, _ = mergeModelCacheIntoDoc(f.doc, &persistedModelCache{Realm: "cn", FetchedAt: modelRefreshNow().Format(time.RFC3339Nano), Models: variantTestModels("retained")})
	r := retryAccountModels("idx", true, true, 0)
	if r.Updated || r.Status != "superseded" || r.Error == "" || len(r.Models) != 1 || r.Models[0].ID != "retained" || calls.Load() != 0 {
		t.Fatalf("retry budget: %+v calls=%d", r, calls.Load())
	}
}
