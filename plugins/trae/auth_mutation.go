package main

// Mutations share a lock. Host inventory and physical files own account state.
import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var accountOperationLocks [256]sync.Mutex

func accountOperationLock(index string) func() {
	var h uint32 = 2166136261
	for _, b := range []byte(strings.TrimSpace(index)) {
		h = (h ^ uint32(b)) * 16777619
	}
	mu := &accountOperationLocks[h%uint32(len(accountOperationLocks))]
	mu.Lock()
	return mu.Unlock
}

type authSnapshot struct {
	File                      pluginapi.HostAuthFileEntry
	Raw                       []byte
	Stored                    *storedAuth
	Auth                      *auth.Auth
	accessToken, refreshToken string // captured before upstream mutates Auth
}

var maintenanceAuthList = hostAuthList
var maintenanceAuthRaw = hostAuthGetRaw
var maintenanceAuthSave = hostAuthSave
var maintenanceRefreshCN = func(a *auth.Auth, skew time.Duration) (bool, error) {
	return upstreamClient.RefreshTokenIfNeeded(a, skew)
}

func freshAuthSnapshot(index string) (*authSnapshot, error) {
	files, err := maintenanceAuthList()
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.AuthIndex != index {
			continue
		}
		raw, err := maintenanceAuthRaw(index)
		if err != nil {
			return nil, err
		}
		if dir := cachedAuthDir(); dir != "" {
			raw, err = readAccountCredential(dir, f.Name)
			if err != nil {
				return nil, err
			}
		}

		a, err := auth.Parse(raw)
		if err != nil {
			return nil, err
		}
		var sa storedAuth
		if err := json.Unmarshal(raw, &sa); err != nil {
			return nil, err
		}
		if a.Variant == "" {
			a.Variant = sniffVariantFromJSON(raw)
		}
		sa.Variant = a.Variant
		sa.Auth.AccessToken, sa.Auth.RefreshToken, sa.Auth.ExpiresAt = a.AccessToken, a.RefreshToken, a.ExpiresAt
		sa.Auth.Domain, sa.Auth.APIHost = a.Domain, a.APIHost
		sa.Account.UID, sa.Account.Nickname, sa.Account.EnterpriseID = a.UID, a.Nickname, a.EnterpriseID
		if a.DeviceID == "" {
			a.DeviceID = sa.Auth.BoundDeviceID
		}
		var flags struct {
			Disabled bool `json:"disabled"`
		}
		_ = json.Unmarshal(raw, &flags)
		f.Disabled = f.Disabled || flags.Disabled
		return &authSnapshot{File: f, Raw: raw, Stored: &sa, Auth: a, accessToken: a.AccessToken, refreshToken: a.RefreshToken}, nil
	}
	return nil, fmt.Errorf("credential removed or stale auth_index")
}

// Caller holds accountOperationLock through snapshot -> upstream -> save.
// Host has no atomic CAS: independent host writes are checked best-effort.
func saveRefreshedAuth(before *authSnapshot, a *auth.Auth) error {
	current, err := freshAuthSnapshot(before.File.AuthIndex)
	if err != nil {
		return err
	}
	if current.File.Name != before.File.Name || current.Auth.UID != before.Auth.UID || current.Auth.Variant != before.Auth.Variant {
		return fmt.Errorf("credential identity changed during refresh")
	}
	if current.accessToken != before.accessToken || current.refreshToken != before.refreshToken {
		return fmt.Errorf("stale refresh: credential tokens changed")
	}
	return maintenanceAuthSave(current.File.Name, preserveSnapshotDisabled(current, mergeAuthStorage(current.Raw, a)))
}

func preserveSnapshotDisabled(s *authSnapshot, raw []byte) []byte {
	if !s.File.Disabled {
		return raw
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return raw
	}
	doc["disabled"] = json.RawMessage("true")
	merged, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return merged
}

func refreshSnapshot(s *authSnapshot, skew time.Duration) (bool, error) {
	if s.File.Disabled {
		return false, fmt.Errorf("account is manually disabled")
	}
	if s.Auth.Variant == variantIntl {
		_, refreshed, err := refreshIntlSnapshot(s, skew)
		return refreshed, err
	} // never send intl tokens to CN refresh
	refreshed, err := maintenanceRefreshCN(s.Auth, skew)
	if err != nil || !refreshed {
		return refreshed, err
	}
	if err = saveRefreshedAuth(s, s.Auth); err != nil {
		return false, err
	}
	s.Stored.Auth.AccessToken, s.Stored.Auth.RefreshToken, s.Stored.Auth.ExpiresAt = s.Auth.AccessToken, s.Auth.RefreshToken, s.Auth.ExpiresAt
	s.accessToken, s.refreshToken = s.Auth.AccessToken, s.Auth.RefreshToken
	return true, nil
}

func refreshExecutorAuth(req pluginapi.ExecutorRequest, fallback *auth.Auth) (*auth.Auth, error) {
	index, resolveErr := resolveAuthIndex(req.AuthID, req.StorageJSON)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if index == "" {
		return fallback, nil
	} // never rotate an unpersistable token
	unlock := accountOperationLock(index)
	defer unlock()
	s, err := freshAuthSnapshot(index)
	if err != nil {
		return nil, err
	}
	if s.File.Disabled {
		return nil, fmt.Errorf("account is manually disabled")
	}
	if _, err = refreshSnapshot(s, defaultRefreshSkew); err != nil {
		return nil, err
	}
	return s.Auth, nil
}
