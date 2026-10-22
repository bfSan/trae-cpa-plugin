package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// OAuth may explicitly create a new credential. Existing credentials keep
// manual disabled/label/unknown metadata; a re-login is not an enable action.
func safeSaveLoginResult(dir string, result *pluginapi.AuthData) error {
	return mutateLoginResult(dir, result, true)
}

// prepareLoginResult refreshes manual metadata for host delivery without any
// filesystem write. Host's later delivery save has no transaction ACK in ABI.
func prepareLoginResult(dir string, result *pluginapi.AuthData) error {
	if strings.TrimSpace(dir) != "" {
		return mutateLoginResult(dir, result, false)
	}
	if result == nil {
		return fmt.Errorf("missing login result")
	}
	files, err := maintenanceAuthList()
	if err != nil {
		if hostAPI == nil {
			return nil
		}
		return fmt.Errorf("host inventory unavailable")
	}
	for _, f := range files {
		if f.Name == result.FileName {
			unlock := accountOperationLock(f.AuthIndex)
			defer unlock()
			s, err := freshAuthSnapshot(f.AuthIndex)
			if err != nil {
				return err
			}
			raw, err := mergeLoginDocuments(s.Raw, result.StorageJSON, s.File.Disabled)
			if err != nil {
				return err
			}
			result.StorageJSON = raw
			applyLoginDocumentLabel(result)
			if s.File.Label != "" {
				result.Label = s.File.Label
			}
			return nil
		}
	}
	return nil
}

func mutateLoginResult(dir string, result *pluginapi.AuthData, persist bool) error {
	if result == nil {
		return fmt.Errorf("missing login result")
	}
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "." || !filepath.IsAbs(dir) {
		return fmt.Errorf("login storage requires an explicit absolute auth directory")
	}
	if configured := cachedAuthDir(); configured != "" && filepath.Clean(configured) != dir {
		return fmt.Errorf("login auth directory changed")
	}
	name := result.FileName
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return fmt.Errorf("invalid login credential name")
	}
	path := filepath.Join(dir, name)
	lockKey := "login:" + name
	disabled := false
	existingIndex := ""
	if files, err := maintenanceAuthList(); err == nil {
		for _, f := range files {
			if f.Name == name {
				lockKey = f.AuthIndex
				existingIndex = f.AuthIndex
				disabled = f.Disabled
				break
			}
		}
	} else if hostAPI != nil {
		return fmt.Errorf("host inventory unavailable")
	}
	unlock := accountOperationLock(lockKey)
	defer unlock()
	if existingIndex != "" {
		current, err := freshAuthSnapshot(existingIndex)
		if err != nil {
			return err
		}
		if current.File.Name != name {
			return fmt.Errorf("login credential identity changed")
		}
		disabled = current.File.Disabled
	}
	var existing []byte
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("login credential is not a regular file")
		}
		existing, err = readAccountCredential(dir, name)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect login credential")
	}
	raw, err := mergeLoginDocuments(existing, result.StorageJSON, disabled)
	if err != nil {
		return err
	}
	if persist {
		if err := writeAccountCredential(dir, name, raw); err != nil {
			return fmt.Errorf("cannot save login credential")
		}
	}
	result.StorageJSON = raw
	applyLoginDocumentLabel(result)
	return nil
}

func applyLoginDocumentLabel(result *pluginapi.AuthData) {
	var doc struct {
		Label string `json:"label"`
	}
	if json.Unmarshal(result.StorageJSON, &doc) == nil && doc.Label != "" {
		result.Label = doc.Label
	}
}

func mergeLoginDocuments(existing, incomingRaw []byte, disabled bool) ([]byte, error) {
	var incoming map[string]json.RawMessage
	if json.Unmarshal(incomingRaw, &incoming) != nil || incoming == nil {
		return nil, fmt.Errorf("invalid login document")
	}
	merged := make(map[string]json.RawMessage)
	for key, value := range incoming {
		merged[key] = value
	}
	if len(existing) > 0 {
		merged = nil // JSON must not reuse or alias the incoming map.
		if json.Unmarshal(existing, &merged) != nil || merged == nil {
			return nil, fmt.Errorf("invalid existing credential")
		}
		for key, value := range incoming {
			if key == "auth" || key == "account" {
				var oldFields, newFields map[string]json.RawMessage
				_ = json.Unmarshal(merged[key], &oldFields)
				_ = json.Unmarshal(value, &newFields)
				if oldFields == nil {
					oldFields = map[string]json.RawMessage{}
				}
				for k, v := range newFields {
					oldFields[k] = v
				}
				merged[key], _ = json.Marshal(oldFields)
			} else if _, present := merged[key]; !present {
				merged[key] = value
			}
		}
	}
	if disabled {
		merged["disabled"] = json.RawMessage("true")
	}
	return json.MarshalIndent(merged, "", "  ")
}
