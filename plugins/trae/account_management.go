package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// patchAccountDocument changes only operator-owned fields. Token refresh and
// model persistence share accountOperationLock and re-read the physical file.
func patchAccountDocument(raw []byte, action, label, reason string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("invalid credential document")
	}
	switch action {
	case "rename":
		label = strings.TrimSpace(label)
		if label == "" || len(label) > 200 {
			return nil, fmt.Errorf("label must contain 1-200 bytes")
		}
		doc["label"], _ = json.Marshal(label)
	case "disable":
		doc["disabled"] = json.RawMessage("true")
		if reason = strings.TrimSpace(reason); reason != "" {
			doc["disabled_reason"], _ = json.Marshal(reason)
		}
	case "enable":
		doc["disabled"] = json.RawMessage("false")
		delete(doc, "disabled_reason")
	default:
		return nil, fmt.Errorf("unsupported account action")
	}
	return json.Marshal(doc)
}

// handleAccountActionQuery handles POST /accounts/action. Deletion deliberately
// uses the host's native authenticated auth-files API, never a plugin file path.
func handleAccountActionQuery(req pluginapi.ManagementRequest) (int, any) {
	var body struct {
		AuthIndex string `json:"auth_index"`
		Action    string `json:"action"`
		Label     string `json:"label"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil || strings.TrimSpace(body.AuthIndex) == "" {
		return http.StatusBadRequest, map[string]any{"error": "auth_index and action required"}
	}
	if body.Action != "rename" && body.Action != "disable" && body.Action != "enable" {
		return http.StatusBadRequest, map[string]any{"error": "unsupported account action"}
	}
	unlock := accountOperationLock(body.AuthIndex)
	defer unlock()
	snapshot, err := freshAuthSnapshot(body.AuthIndex)
	if err != nil {
		return http.StatusConflict, map[string]any{"error": err.Error()}
	}
	raw, err := patchAccountDocument(snapshot.Raw, body.Action, body.Label, body.Reason)
	if err != nil {
		return http.StatusBadRequest, map[string]any{"error": err.Error()}
	}
	if err = maintenanceAuthSave(snapshot.File.Name, raw); err != nil {
		return http.StatusInternalServerError, map[string]any{"error": err.Error()}
	}
	return http.StatusOK, map[string]any{"ok": true, "auth_index": body.AuthIndex, "action": body.Action}
}

// No delete/recreate fallback is allowed on failure. The host owns storage and
// unknown credential fields are retained by the raw-message patch above.
