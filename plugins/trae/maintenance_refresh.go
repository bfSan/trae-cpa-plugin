package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	intl "github.com/bfSan/trae-cpa-plugin/plugins/trae/intlupstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Resolve host identity, never select a different account. ID/name wins; a
// missing ID may resolve only one exact (variant, UID) match from storage.
func resolveAuthIndex(id string, raw []byte) (string, error) {
	if id == "" && hostAPI == nil {
		return "", nil
	} // standalone mock executor; no token rotation
	files, err := maintenanceAuthList()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if id != "" && (f.AuthIndex == id || f.ID == id || f.Name == id) {
			return f.AuthIndex, nil
		}
	}
	if id != "" {
		return "", fmt.Errorf("credential removed or unknown auth ID")
	}
	a, err := auth.Parse(raw)
	if err != nil || a.UID == "" {
		return "", fmt.Errorf("missing durable account identity")
	}
	variant := a.Variant
	if variant == "" {
		variant = sniffVariantFromJSON(raw)
	}
	found := ""
	for _, f := range files {
		s, err := freshAuthSnapshot(f.AuthIndex)
		if err != nil {
			continue
		}
		if s.Auth.UID == a.UID && s.Auth.Variant == variant {
			if found != "" {
				return "", fmt.Errorf("ambiguous account identity")
			}
			found = f.AuthIndex
		}
	}
	if found == "" {
		return "", fmt.Errorf("credential removed or unknown identity")
	}
	return found, nil
}
func executorObservationIndex(req pluginapi.ExecutorRequest) string {
	index, _ := resolveAuthIndex(req.AuthID, req.StorageJSON)
	return index
}

func mergeIntlAuthStorage(raw []byte, a *intl.Auth) []byte {
	merged := mergeAuthStorage(raw, &auth.Auth{AccessToken: a.AccessToken, RefreshToken: a.RefreshToken, ExpiresAt: a.ExpiresAt, Domain: a.Domain, APIHost: a.APIHost, MachineID: a.MachineID, DeviceID: a.DeviceID, UID: a.UID, EnterpriseID: a.EnterpriseID, Nickname: a.Nickname, Variant: variantIntl})
	var doc map[string]any
	if json.Unmarshal(merged, &doc) != nil {
		return nil
	}
	fields, ok := doc["auth"].(map[string]any)
	if !ok {
		fields = doc
	}
	for k, v := range map[string]string{"region": a.Region, "scope": a.Scope, "tenant": a.Tenant, "appLanguage": a.AppLanguage, "appVersion": a.AppVersion} {
		if v != "" {
			fields[k] = v
		}
	}
	out, _ := json.Marshal(doc)
	return out
}
func saveRefreshedIntlAuth(before *authSnapshot, a *intl.Auth) error {
	current, err := freshAuthSnapshot(before.File.AuthIndex)
	if err != nil {
		return err
	}
	if current.File.Name != before.File.Name || current.Auth.UID != before.Auth.UID || current.Auth.Variant != variantIntl {
		return fmt.Errorf("credential identity changed during refresh")
	}
	if current.accessToken != before.accessToken || current.refreshToken != before.refreshToken {
		return fmt.Errorf("stale refresh: credential tokens changed")
	}
	return maintenanceAuthSave(current.File.Name, preserveSnapshotDisabled(current, mergeIntlAuthStorage(current.Raw, a)))
}
func refreshIntlSnapshot(s *authSnapshot, skew time.Duration) (*intl.Auth, bool, error) {
	if s.File.Disabled {
		return nil, false, fmt.Errorf("account is manually disabled")
	}
	a, err := intlparseStoredAuth(s.Raw)
	if err != nil {
		return nil, false, err
	}
	refreshed, err := intlupstreamClient.RefreshTokenIfNeeded(a, skew)
	if err != nil {
		return nil, false, err
	}
	if refreshed {
		if err = saveRefreshedIntlAuth(s, a); err != nil {
			return nil, false, err
		}
	}
	s.Stored.Auth.AccessToken, s.Stored.Auth.RefreshToken, s.Stored.Auth.ExpiresAt = a.AccessToken, a.RefreshToken, a.ExpiresAt
	s.Auth.AccessToken, s.Auth.RefreshToken, s.Auth.ExpiresAt = a.AccessToken, a.RefreshToken, a.ExpiresAt
	s.accessToken, s.refreshToken = a.AccessToken, a.RefreshToken
	return a, refreshed, nil
}
func refreshIntlExecutorAuth(req pluginapi.ExecutorRequest, fallback *intl.Auth) (*intl.Auth, error) {
	index, err := resolveAuthIndex(req.AuthID, req.StorageJSON)
	if err != nil {
		return nil, err
	}
	if index == "" {
		return fallback, nil
	}
	unlock := accountOperationLock(index)
	defer unlock()
	s, err := freshAuthSnapshot(index)
	if err != nil {
		return nil, err
	}
	if s.Auth.Variant != variantIntl {
		return nil, fmt.Errorf("account realm changed")
	}
	a, _, err := refreshIntlSnapshot(s, defaultRefreshSkew)
	return a, err
}

func refreshAuthRequest(request []byte) ([]byte, error) {
	captureAuthDir(request)
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	index, err := resolveAuthIndex(req.AuthID, req.StorageJSON)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(index) == "" {
		return nil, fmt.Errorf("auth refresh requires durable host identity")
	}
	unlock := accountOperationLock(index)
	defer unlock()
	s, err := freshAuthSnapshot(index)
	if err != nil {
		return nil, err
	}
	if s.File.Disabled {
		return nil, fmt.Errorf("account is manually disabled")
	}
	// Save to the host immediately: rotating refresh tokens cannot wait for a
	// later executor. Return the fresh merged document for the host RPC contract.
	if s.Auth.Variant == variantIntl {
		_, _, err = refreshIntlSnapshot(s, defaultRefreshSkew)
	} else {
		_, err = refreshSnapshot(s, defaultRefreshSkew)
	}
	if err != nil {
		return nil, err
	}
	current, err := freshAuthSnapshot(index)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Provider: providerName, FileName: current.File.Name, Label: current.File.Label, StorageJSON: current.Raw}, NextRefreshAfter: time.Now().Add(12 * time.Hour).UTC()})
}
