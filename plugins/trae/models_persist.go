// Account-scoped last-good catalogs. Candidate unions live only in management.
package main

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// modelCacheDocKey is the top-level key stamped into credential files. It
// rides along every save because the merge-based write paths
// (mergeAuthStorage, heal, adopt) preserve unknown top-level keys as raw JSON.
const modelCacheDocKey = "model_cache"

// preservedPluginDocKeys lists plugin-stamped top-level credential keys that
// must survive EVERY hostAuthSave funnel call — including the typed rebuilds
// (intlpersistRefreshedAuthTo, import) which construct a fresh document and
// would otherwise silently drop them. The whitelist (not a blanket merge) is
// deliberate: a rebuild must not smuggle arbitrary stale keys from a
// same-named older file.
// v0.12.68 (issue #25): beyond the plugin-stamped model_cache, the list now
// carries host-owned operator fields CPA writes into auth files via the auth
// manager (proxy_url / weight / priority / prefix / label / request_retry /
// headers) — every plugin-side rewrite used to reset them to defaults.
var preservedPluginDocKeys = []string{
	modelCacheDocKey,
	"proxy_url",
	"weight",
	"priority",
	"prefix",
	"label",
	"request_retry",
	"headers",
}

// persistedModelCache is the on-disk snapshot shape.
type persistedModelCache struct {
	// Realm is the variant namespace the catalog was discovered under
	// ("cn" | "solo" | "intl").
	Realm string `json:"realm"`
	// FetchedAt is the discovery time (RFC3339 UTC).
	FetchedAt string `json:"fetched_at"`
	// Models is the advertised list (post-suffix, pre exclusion).
	Models []pluginapi.ModelInfo `json:"models"`
}

// Test seams — the real host RPC bridge is unavailable in unit tests.
var (
	hostAuthListFn = hostAuthList
	hostAuthRawFn  = hostAuthGetRaw
	hostAuthSaveFn = hostAuthSave
	intlSaveFn     = intlhostAuthSave
)

// readModelCacheDoc extracts the model_cache snapshot from one credential
// document. Returns nil when absent, malformed, or empty — a snapshot with
// no models is treated as "never persisted" so it can never shadow a peer's
// real catalog.
func readModelCacheDoc(doc []byte) *persistedModelCache {
	if len(doc) == 0 {
		return nil
	}
	var probe struct {
		Cache *persistedModelCache `json:"model_cache"`
	}
	if err := json.Unmarshal(doc, &probe); err != nil || probe.Cache == nil {
		return nil
	}
	c := probe.Cache
	if strings.TrimSpace(c.Realm) == "" || len(c.Models) == 0 {
		return nil
	}
	return c
}

// persistedSnapshotForStorage resolves the last persisted snapshot for the
// asking credential's variant: its own document first, then its own physical file. Peer catalogs are never
// authorization evidence, even within the same variant.
func persistedSnapshotForStorage(storageJSON []byte, variant string) ([]pluginapi.ModelInfo, bool) {
	if snap := readModelCacheDoc(storageJSON); snap != nil && snap.Realm == variant {
		return snap.Models, true
	}
	// The host may hand us a storage projection without plugin keys. Only the
	// same physical credential can supply last-good data, never a regional peer.
	_, doc, ok := locateCredentialFile(storageJSON)
	if ok {
		if snap := readModelCacheDoc(doc); snap != nil && snap.Realm == variant {
			return snap.Models, true
		}
	}
	return nil, false
}

// persistedFetchedAt renders the snapshot's discovery time for comparison;
// unparsable timestamps sort oldest ("").
func persistedFetchedAt(c *persistedModelCache) string {
	if c == nil {
		return ""
	}
	return c.FetchedAt
}

// persistModelSnapshot locates the asking credential's file and merges the
// fresh snapshot into it. Best-effort: failures are logged, never propagated
// — a persist miss only means the next restart re-discovers from scratch.
func persistModelSnapshot(storageJSON []byte, variant string, models []pluginapi.ModelInfo) {
	name, _, ok := locateCredentialFile(storageJSON)
	if !ok {
		return
	}
	files, err := hostAuthListFn()
	if err != nil {
		return
	}
	index := ""
	for _, f := range files {
		if f.Name == name {
			index = f.AuthIndex
			break
		}
	}
	if index == "" {
		return
	}
	unlock := accountOperationLock(index)
	defer unlock()
	snapshot, err := freshAuthSnapshot(index)
	if err != nil {
		log.Printf("models: snapshot skipped: %v", err)
		return
	}
	// Discovery must still belong to the same token/variant after waiting for
	// concurrent maintenance; otherwise do not stamp a stale entitlement.
	if snapshot.Auth.AccessToken != extractTraeAccessToken(storageJSON) || snapshot.Auth.Variant != variant {
		return
	}
	doc := snapshot.Raw
	if existing := readModelCacheDoc(doc); existing != nil && existing.Realm == variant && samePersistedModels(existing.Models, models) {
		return
	}
	merged, err := mergeModelCacheIntoDoc(doc, &persistedModelCache{Realm: variant, FetchedAt: time.Now().UTC().Format(time.RFC3339), Models: models})
	if err != nil {
		log.Printf("models: snapshot merge failed: %v", err)
		return
	}
	if err = maintenanceAuthSave(snapshot.File.Name, merged); err != nil {
		log.Printf("models: snapshot save failed: %v", err)
	}
}

// locateCredentialFile matches the asking credential to its file by access
// token — the only stable identifier that survives both storage shapes
// (nested trae-*.json and flat CPA-panel imports) and every naming scheme
// (trae-<uid>, trae-solo-cn-<uid>, trae-intl-<uid>).
func locateCredentialFile(storageJSON []byte) (name string, doc []byte, ok bool) {
	tok := extractTraeAccessToken(storageJSON)
	if strings.TrimSpace(tok) == "" {
		return "", nil, false
	}
	files, err := hostAuthListFn()
	if err != nil || len(files) == 0 {
		return "", nil, false
	}
	for _, f := range files {
		phys, err := hostAuthRawFn(f.AuthIndex)
		if err != nil || len(phys) == 0 {
			continue
		}
		if extractTraeAccessToken(phys) == tok && sniffVariantFromJSON(phys) == sniffVariantFromJSON(storageJSON) {
			return f.Name, phys, true
		}
	}
	return "", nil, false
}

// extractTraeAccessToken reads the access token from a credential document,
// nested (auth.accessToken) or flat (accessToken).
func extractTraeAccessToken(raw []byte) string {
	var probe struct {
		Auth struct {
			AccessToken string `json:"accessToken"`
		} `json:"auth"`
		AccessToken string `json:"accessToken"`
	}
	_ = json.Unmarshal(raw, &probe)
	if strings.TrimSpace(probe.Auth.AccessToken) != "" {
		return probe.Auth.AccessToken
	}
	return probe.AccessToken
}

// mergeModelCacheIntoDoc stamps the snapshot into a credential document,
// preserving every other top-level key byte-for-byte (note, parity extras,
// panel-managed fields).
func mergeModelCacheIntoDoc(doc []byte, cache *persistedModelCache) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		return nil, err
	}
	m[modelCacheDocKey] = raw
	return json.Marshal(m)
}

// samePersistedModels compares two snapshots field-completely (not just by
// model id): a renamed display name or changed context limit is a real
// catalog change worth one save.
func samePersistedModels(a, b []pluginapi.ModelInfo) bool {
	if len(a) != len(b) {
		return false
	}
	aj, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aj, bj)
}

// preservePluginDocKeys re-injects whitelisted plugin-stamped keys that the
// incoming document is missing but the current physical file carries. Wired
// into the hostAuthSave / intlhostAuthSave funnels so typed rebuilds
// (intlpersistRefreshedAuthTo, import) keep the model_cache snapshot alive —
// without it, every intl token refresh would silently wipe the persisted
// catalog. Keys the caller explicitly set always win over the persisted copy.
func preservePluginDocKeys(name string, doc []byte) []byte {
	if len(doc) == 0 || name == "" {
		return doc
	}
	var have map[string]json.RawMessage
	if err := json.Unmarshal(doc, &have); err != nil {
		return doc
	}
	needed := make([]string, 0, len(preservedPluginDocKeys))
	for _, k := range preservedPluginDocKeys {
		if _, ok := have[k]; !ok {
			needed = append(needed, k)
		}
	}
	if len(needed) == 0 {
		return doc
	}
	physical := physicalDocByName(name)
	if len(physical) == 0 {
		return doc
	}
	var prev map[string]json.RawMessage
	if err := json.Unmarshal(physical, &prev); err != nil {
		return doc
	}
	restored := false
	for _, k := range needed {
		raw, ok := prev[k]
		if !ok || len(raw) == 0 {
			continue
		}
		have[k] = raw
		restored = true
	}
	if !restored {
		return doc
	}
	merged, err := json.Marshal(have)
	if err != nil {
		return doc
	}
	return merged
}

// physicalDocByName resolves one credential document by exact file name
// (best-effort; empty when the name is unknown — e.g. a first save).
func physicalDocByName(name string) []byte {
	files, err := hostAuthListFn()
	if err != nil {
		return nil
	}
	for _, f := range files {
		if !strings.EqualFold(strings.TrimSpace(f.Name), name) {
			continue
		}
		phys, err := hostAuthRawFn(f.AuthIndex)
		if err != nil {
			return nil
		}
		return phys
	}
	return nil
}

// modelStorageWithAccessToken updates an in-memory lookup document after a
// safely persisted refresh. It is never written as a credential replacement.
func modelStorageWithAccessToken(raw []byte, token string) []byte {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return raw
	}
	value, _ := json.Marshal(token)
	if nested, ok := doc["auth"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(nested, &fields) == nil && fields != nil {
			fields["accessToken"] = value
			doc["auth"], _ = json.Marshal(fields)
		} else {
			return raw
		}
	} else {
		doc["accessToken"] = value
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}
