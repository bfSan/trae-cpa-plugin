package main

// Model discovery never rotates tokens or holds the account lock over network IO.
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	intl "github.com/bfSan/trae-cpa-plugin/plugins/trae/intlupstream"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const modelCacheSchemaVersion = 2
const modelRefreshInterval = 24 * time.Hour
const modelRefreshStateDocKey = "model_refresh_state"

type modelRefreshState struct {
	SchemaVersion int    `json:"schema_version"`
	Realm         string `json:"realm"`
	LastAttemptAt string `json:"last_attempt_at"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}
type modelRefreshResult struct {
	Updated       bool                  `json:"updated"`
	Status        string                `json:"status"`
	Error         string                `json:"error,omitempty"`
	FetchedAt     string                `json:"fetched_at,omitempty"`
	NextRefreshAt string                `json:"next_refresh_at,omitempty"`
	Models        []pluginapi.ModelInfo `json:"-"`
	Raw           []byte                `json:"-"`
}
type modelRefreshFlight struct {
	done   chan struct{}
	result modelRefreshResult
	// Published before HostSave: synchronous registration watchers cannot join
	// the very flight that is saving their credential.
	visible modelRefreshResult
}

var modelRefreshFlights = struct {
	sync.Mutex
	active map[string]*modelRefreshFlight
}{active: make(map[string]*modelRefreshFlight)}
var modelRefreshNow = time.Now
var modelRefreshCreditsFn = refreshAccountCredits
var fetchIntlModelsFn = func(a *intl.Auth) ([]string, error) { return intlupstreamClient.FetchModels(a) }

func readModelRefreshState(raw []byte) modelRefreshState {
	var doc struct {
		State modelRefreshState `json:"model_refresh_state"`
	}
	_ = json.Unmarshal(raw, &doc)
	return doc.State
}
func modelRefreshDue(raw []byte, realm string, now time.Time) bool {
	state := readModelRefreshState(raw)
	if state.Realm == realm && state.SchemaVersion == modelCacheSchemaVersion {
		if last, err := time.Parse(time.RFC3339Nano, state.LastAttemptAt); err == nil {
			return last.After(now.Add(5*time.Minute)) || !now.Before(last.Add(modelRefreshInterval))
		}
	}
	if cache := readModelCacheDoc(raw); cache != nil && cache.Realm == realm && cache.SchemaVersion == modelCacheSchemaVersion {
		if last, err := time.Parse(time.RFC3339Nano, cache.FetchedAt); err == nil {
			return last.After(now.Add(5*time.Minute)) || !now.Before(last.Add(modelRefreshInterval))
		}
	}
	return true
}
func modelResultForDoc(raw []byte, realm, status string) modelRefreshResult {
	r := modelRefreshResult{Status: status, Raw: raw}
	if c := readModelCacheDoc(raw); c != nil && c.Realm == realm {
		r.Models, r.FetchedAt = c.Models, c.FetchedAt
		if at, err := time.Parse(time.RFC3339Nano, c.FetchedAt); err == nil {
			r.NextRefreshAt = at.Add(modelRefreshInterval).UTC().Format(time.RFC3339Nano)
		}
	}
	st := readModelRefreshState(raw)
	if st.Realm == realm && st.SchemaVersion == modelCacheSchemaVersion {
		if at, err := time.Parse(time.RFC3339Nano, st.LastAttemptAt); err == nil {
			r.NextRefreshAt = at.Add(modelRefreshInterval).UTC().Format(time.RFC3339Nano)
		}
	}
	return r
}

// Shared daily/manual/auth-refresh entry point. Call without accountOperationLock.
// Failure may return last-good models, but never Updated=true.
func refreshAccountModels(index string, force bool) modelRefreshResult {
	return refreshAccountModelsMode(index, force, true)
}

// Nonwaiting mode is for model.for_auth, including synchronous HostSave watchers.
func refreshAccountModelsMode(index string, force, wait bool) modelRefreshResult {
	return refreshAccountModelsAttempt(index, force, wait, 1)
}

// At most one restart after joining a superseded flight. Continuous token churn
// must not turn one user action into an unbounded stream of catalog requests.
func retryAccountModels(index string, force, wait bool, retries int) modelRefreshResult {
	if retries > 0 {
		return refreshAccountModelsAttempt(index, force, wait, retries-1)
	}
	result := modelRefreshResult{Status: "superseded", Error: "credential changed repeatedly during model refresh; retry after credential updates settle"}
	if current, err := freshAuthSnapshot(index); err == nil && !current.File.Disabled {
		result = modelResultForDoc(current.Raw, current.Auth.Variant, "superseded")
		result.Error = "credential changed repeatedly during model refresh; retry after credential updates settle"
	}
	return result
}
func refreshAccountModelsAttempt(index string, force, wait bool, retries int) modelRefreshResult {
	modelRefreshFlights.Lock()
	if f := modelRefreshFlights.active[index]; f != nil {
		visible := f.visible
		modelRefreshFlights.Unlock()
		if !wait {
			return visible
		}
		<-f.done
		if f.result.Status == "superseded" {
			return retryAccountModels(index, force, wait, retries)
		}
		// A credential refresh may have completed just after the joined flight
		// committed. Its caller still needs discovery with the NEW token.
		if len(f.result.Raw) > 0 {
			latest, err := freshAuthSnapshot(index)
			previous, parseErr := auth.Parse(f.result.Raw)
			if err == nil && parseErr == nil && (latest.accessToken != previous.AccessToken || latest.refreshToken != previous.RefreshToken || latest.Auth.Variant != sniffVariantFromJSON(f.result.Raw)) {
				return retryAccountModels(index, force, wait, retries)
			}
		}
		return f.result
	}
	f := &modelRefreshFlight{done: make(chan struct{}), visible: modelRefreshResult{Status: "in_progress"}}
	modelRefreshFlights.active[index] = f
	modelRefreshFlights.Unlock()
	result := performAccountModelRefresh(index, force, wait, f)
	modelRefreshFlights.Lock()
	f.result = result
	delete(modelRefreshFlights.active, index)
	close(f.done)
	modelRefreshFlights.Unlock()
	return result
}
func publishModelRefresh(f *modelRefreshFlight, r modelRefreshResult) {
	modelRefreshFlights.Lock()
	f.visible = r
	modelRefreshFlights.Unlock()
}

// Same shard mapping as accountOperationLock. TryLock avoids token-save watcher
// reentry deadlocking even before a model flight has started.
func modelAccountLock(index string, wait bool) (func(), bool) {
	if wait {
		return accountOperationLock(index), true
	}
	var h uint32 = 2166136261
	for _, b := range []byte(strings.TrimSpace(index)) {
		h = (h ^ uint32(b)) * 16777619
	}
	mu := &accountOperationLocks[h%uint32(len(accountOperationLocks))]
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}
func modelSnapshotMatches(before, current *authSnapshot) bool {
	return !current.File.Disabled && before.File.Name == current.File.Name && before.Auth.UID == current.Auth.UID && before.Auth.Variant == current.Auth.Variant && before.accessToken == current.accessToken && before.refreshToken == current.refreshToken && before.Auth.Domain == current.Auth.Domain && before.Auth.APIHost == current.Auth.APIHost
}
func mergeModelRefreshState(raw []byte, state modelRefreshState) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("empty credential document")
	}
	value, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	doc[modelRefreshStateDocKey] = value
	return json.Marshal(doc)
}
func performAccountModelRefresh(index string, force, wait bool, flight *modelRefreshFlight) modelRefreshResult {
	unlock, locked := modelAccountLock(index, wait)
	if !locked {
		return modelRefreshResult{Status: "busy"}
	}
	before, err := freshAuthSnapshot(index)
	if err != nil {
		unlock()
		return modelRefreshResult{Status: "skipped", Error: err.Error()}
	}
	result := modelResultForDoc(before.Raw, before.Auth.Variant, "not_due")
	publishModelRefresh(flight, result)
	if before.File.Disabled {
		unlock()
		result.Status = "skipped"
		result.Error = "account is manually disabled"
		return result
	}
	now := modelRefreshNow().UTC()
	if !force && !modelRefreshDue(before.Raw, before.Auth.Variant, now) {
		unlock()
		return result
	}
	state := modelRefreshState{SchemaVersion: modelCacheSchemaVersion, Realm: before.Auth.Variant, LastAttemptAt: now.Format(time.RFC3339Nano), Status: "in_progress"}
	// Checkpoint metadata BEFORE network: a restart cannot cause minute retries.
	checkpoint, err := mergeModelRefreshState(before.Raw, state)
	if err == nil {
		err = maintenanceAuthSave(before.File.Name, checkpoint)
	}
	unlock()
	if err != nil {
		result.Status = "failed"
		result.Error = "cannot persist model refresh attempt: " + err.Error()
		return result
	}
	result.NextRefreshAt = now.Add(modelRefreshInterval).Format(time.RFC3339Nano)
	var models []pluginapi.ModelInfo
	var facts map[string]upstream.ModelFacts
	if before.Auth.AccessToken == "" {
		err = fmt.Errorf("missing access token")
	} else if before.Auth.ExpiresAt > 0 && before.Auth.ExpiresAt <= now.Unix() {
		err = fmt.Errorf("access token expired; refresh account credentials before model discovery")
	} else {
		models, facts, err = discoverAccountModels(before.Auth, before.Raw)
	}
	if err == nil && len(models) == 0 {
		err = fmt.Errorf("upstream returned an empty model catalog")
	}
	state.Status = "updated"
	for _, fact := range facts {
		if fact.CapabilityStatus == "partial" {
			state.Status = "partial"
			state.Error = "auxiliary capability catalog unavailable; primary model catalog updated"
			break
		}
	}
	if err != nil {
		state.Status = "failed"
		state.Error = err.Error()
	}
	unlock = accountOperationLock(index)
	defer unlock()
	current, currentErr := freshAuthSnapshot(index)
	if currentErr != nil || !modelSnapshotMatches(before, current) {
		result.Updated, result.Status, result.Error = false, "superseded", "credential removed, disabled, or changed during model refresh"
		result.Models, result.Raw = nil, nil
		return result
	}
	merged, mergeErr := mergeModelRefreshState(current.Raw, state)
	if mergeErr == nil && err == nil {
		merged, mergeErr = mergeModelCacheIntoDoc(merged, &persistedModelCache{SchemaVersion: modelCacheSchemaVersion, Realm: before.Auth.Variant, FetchedAt: now.Format(time.RFC3339Nano), Models: models, Facts: facts})
	}
	if mergeErr != nil {
		result.Status, result.Error = "failed", mergeErr.Error()
		return result
	}
	result = modelResultForDoc(merged, before.Auth.Variant, state.Status)
	result.Updated, result.Error = err == nil, state.Error
	publishModelRefresh(flight, result)
	if saveErr := maintenanceAuthSave(current.File.Name, merged); saveErr != nil {
		result = modelResultForDoc(current.Raw, before.Auth.Variant, "failed")
		result.Error = "cannot persist model catalog: " + saveErr.Error()
	}
	return result
}

func discoverAccountModels(a *auth.Auth, raw []byte) ([]pluginapi.ModelInfo, map[string]upstream.ModelFacts, error) {
	if a.Variant == variantIntl {
		ia, err := intlparseStoredAuth(raw)
		if err != nil {
			return nil, nil, err
		}
		dynamic, err := fetchIntlModelsFn(ia)
		if err != nil {
			return nil, nil, err
		}
		if len(dynamic) == 0 {
			return nil, nil, fmt.Errorf("upstream returned an empty model catalog")
		}
		out := make([]pluginapi.ModelInfo, 0, len(dynamic)+2)
		seen := map[string]bool{"auto": true, "work": true}
		for _, id := range dynamic {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, pluginapi.ModelInfo{ID: id + "-intl", Name: id + "-intl", OwnedBy: intlproviderName})
		}
		out = append(out, pluginapi.ModelInfo{ID: "auto", Name: "auto (server pick)"}, pluginapi.ModelInfo{ID: "work", Name: "work (fast mode)"})
		return out, nil, nil
	}
	dynamic, err := fetchModelsFn(a)
	if err != nil {
		return nil, nil, err
	}
	dynamic = applyModelContextEvidence(a, raw, dynamic)
	suffix := ""
	if a.Variant == variantSolo {
		suffix = modelSuffixSolo
	}
	out := make([]pluginapi.ModelInfo, 0, len(dynamic))
	facts := make(map[string]upstream.ModelFacts)
	seen := make(map[string]bool)
	for _, m := range dynamic {
		if m.ID == "" || m.ID == "auto" || m.ID == "work" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		id := m.ID + suffix
		out = append(out, pluginapi.ModelInfo{ID: id, Name: m.Name, ContextLength: m.ContextWindow, MaxCompletionTokens: m.MaxTokens})
		facts[id] = m.Facts
	}
	return out, facts, nil
}

// Exact token + realm matching cannot attach an unsaved credential to a peer.
func modelAuthIndexForStorage(raw []byte) string {
	files, err := maintenanceAuthList()
	if err != nil {
		return ""
	}
	token, realm := extractTraeAccessToken(raw), sniffVariantFromJSON(raw)
	if token == "" {
		return ""
	}
	found := ""
	for _, f := range files {
		doc, err := maintenanceAuthRaw(f.AuthIndex)
		if err != nil {
			continue
		}
		if extractTraeAccessToken(doc) == token && sniffVariantFromJSON(doc) == realm {
			if found != "" {
				return ""
			}
			found = f.AuthIndex
		}
	}
	return found
}
func accountModelsForStorage(a *auth.Auth, raw []byte, force bool, authIDs ...string) []pluginapi.ModelInfo {
	var flags struct {
		Disabled bool `json:"disabled"`
	}
	_ = json.Unmarshal(raw, &flags)
	if flags.Disabled {
		return []pluginapi.ModelInfo{}
	}
	index := modelAuthIndexForStorage(raw)
	if len(authIDs) > 0 && authIDs[0] != "" {
		resolved, err := resolveAuthIndex(authIDs[0], raw)
		if err != nil || resolved == "" {
			return []pluginapi.ModelInfo{}
		}
		index = resolved
	}
	if index != "" {
		current, err := freshAuthSnapshot(index)
		if err != nil || current.File.Disabled || current.Auth.Variant != a.Variant {
			return []pluginapi.ModelInfo{}
		}
		result := refreshAccountModelsMode(index, force, false)
		if result.Status == "superseded" || result.Status == "skipped" {
			return []pluginapi.ModelInfo{}
		}
		if result.Models != nil && sniffVariantFromJSON(result.Raw) == current.Auth.Variant && extractTraeAccessToken(result.Raw) == current.accessToken {
			return applyModelOrder(result.Models)
		}
		// A nonwaiting registration can see a flight from before token/realm
		// rotation. Never use that flight's catalog for the current credential.
		if snap := readModelCacheDoc(current.Raw); snap != nil && snap.Realm == a.Variant {
			return applyModelOrder(snap.Models)
		}
		return []pluginapi.ModelInfo{}
	}
	// With a live host, an unresolved credential may have been deleted. Do not
	// resurrect it by issuing API calls from a stale registration document.
	if hostAPI != nil {
		return []pluginapi.ModelInfo{}
	}
	// Standalone callers can use their own snapshot but never a peer's catalog.
	if c := readModelCacheDoc(raw); c != nil && c.Realm == a.Variant && !force {
		return applyModelOrder(c.Models)
	}
	models, _, err := discoverAccountModels(a, raw)
	if err == nil && len(models) > 0 {
		return applyModelOrder(models)
	}
	if snap, ok := persistedSnapshotForStorage(raw, a.Variant); ok {
		return applyModelOrder(snap)
	}
	return []pluginapi.ModelInfo{}
}
func refreshModelCatalogsDaily(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	files, err := maintenanceAuthList()
	if err != nil {
		return
	}
	for _, f := range files {
		if ctx.Err() != nil {
			return
		}
		if f.Disabled {
			continue
		}
		s, err := freshAuthSnapshot(f.AuthIndex)
		if err != nil || s.File.Disabled || !modelRefreshDue(s.Raw, s.Auth.Variant, now) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		refreshAccountModels(f.AuthIndex, false)
	}
}
