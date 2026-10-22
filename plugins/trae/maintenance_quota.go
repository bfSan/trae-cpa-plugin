package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Observations, not an account pool: no pick, ranking, rotation, tiers or
// automatic disabled writes. A zero balance snapshot alone never rejects chat.
type quotaObservation struct {
	AuthIndex  string    `json:"auth_index"`
	Model      string    `json:"model,omitempty"`
	Reason     string    `json:"reason"`
	ObservedAt time.Time `json:"observed_at"`
	Until      time.Time `json:"until"` // observation TTL, NOT a guessed reset time
}

var quotaObservations sync.Map
var quotaObservationInsertMu sync.Mutex

const quotaObservationTTL = 5 * time.Minute
const maxQuotaObservations = 2048

// Admission bounds memory; it never evicts live evidence or ranks accounts.
func storeQuotaObservation(observation quotaObservation) bool {
	if len(observation.AuthIndex) > 1024 || len(observation.Model) > 1024 {
		return false
	}
	quotaObservationInsertMu.Lock()
	defer quotaObservationInsertMu.Unlock()
	now := time.Now()
	count := 0
	quotaObservations.Range(func(key, value any) bool {
		if !now.Before(value.(quotaObservation).Until) {
			quotaObservations.CompareAndDelete(key, value)
		} else {
			count++
		}
		return true
	})
	key := quotaObservationKey(observation.AuthIndex, observation.Model)
	if _, exists := quotaObservations.Load(key); !exists && count >= maxQuotaObservations {
		return false
	}
	quotaObservations.Store(key, observation)
	return true
}

func quotaObservationKey(index, model string) string { return index + "\x00" + model }
func observeChatFailure(index, model string, kind upstream.ErrKind) {
	if index == "" || kind != upstream.ErrPlanLimit {
		return
	}
	now := time.Now()
	storeQuotaObservation(quotaObservation{AuthIndex: index, Model: model, Reason: "upstream_quota_rejection", ObservedAt: now, Until: now.Add(quotaObservationTTL)})
}
func quotaGuard(index, model string) bool {
	value, ok := quotaObservations.Load(quotaObservationKey(index, model))
	if !ok {
		return false
	}
	observation := value.(quotaObservation)
	if !time.Now().Before(observation.Until) {
		quotaObservations.CompareAndDelete(quotaObservationKey(index, model), value)
		return false
	}
	return true
}
func releaseQuotaObservation(index string) {
	quotaObservations.Range(func(key, value any) bool {
		if value.(quotaObservation).AuthIndex == index {
			quotaObservations.CompareAndDelete(key, value)
		}
		return true
	})
}
func buildMaintenanceStatus() map[string]any {
	files, err := maintenanceAuthList()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	rows := []map[string]any{}
	for _, f := range files {
		row := map[string]any{"auth_index": f.AuthIndex, "name": f.Name, "disabled": f.Disabled, "cooling": false}
		if s, err := freshAuthSnapshot(f.AuthIndex); err == nil {
			row["uid"] = s.Auth.UID
			row["disabled"] = s.File.Disabled
		}
		facts := []quotaObservation{}
		quotaObservations.Range(func(_, v any) bool {
			q := v.(quotaObservation)
			if q.AuthIndex == f.AuthIndex && time.Now().Before(q.Until) {
				facts = append(facts, q)
				row["cooling"] = true
				row["reason"] = q.Reason
				row["until"] = q.Until
			}
			return true
		})
		row["observations"] = facts
		rows = append(rows, row)
	}
	return map[string]any{"provider": providerName, "accounts": rows, "server_time": time.Now().Format(time.RFC3339)}
}
func releaseAccountObservation(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		AuthIndex string `json:"auth_index"`
		UID       string `json:"uid"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return map[string]any{"error": "invalid JSON", "released": false}
	}
	index := strings.TrimSpace(body.AuthIndex)
	if index == "" && body.UID != "" {
		files, err := maintenanceAuthList()
		if err != nil {
			return map[string]any{"error": err.Error(), "released": false}
		}
		for _, f := range files {
			s, err := freshAuthSnapshot(f.AuthIndex)
			if err == nil && s.Auth.UID == body.UID {
				if index != "" {
					return map[string]any{"error": "ambiguous uid; supply auth_index", "released": false}
				}
				index = f.AuthIndex
			}
		}
	}
	unlock := accountOperationLock(index)
	defer unlock()
	s, err := freshAuthSnapshot(index)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("resolve account: %v", err), "released": false}
	}
	releaseQuotaObservation(index)
	return map[string]any{"released": true, "auth_index": index, "uid": s.Auth.UID, "disabled": s.File.Disabled}
}
