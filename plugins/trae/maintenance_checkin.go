package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
)

// Each account has its own finite budget, shared by manual/auto/retry. Once a
// claim might have reached upstream, later attempts ONLY verify that claim.
const maxDailyCheckinAttempts = 4

var checkinDayLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

type checkinAttemptState struct {
	day       string
	attempts  int
	pending   bool
	confirmed bool
	next      time.Time
}

var checkinAttempts sync.Map // auth_index -> immutable *checkinAttemptState
var checkinAttemptInsertMu sync.Mutex

const maxCheckinAttemptEntries = 2048

func storeCheckinAttempt(index string, state checkinAttemptState) bool {
	if len(index) > 1024 {
		return false
	}
	checkinAttemptInsertMu.Lock()
	defer checkinAttemptInsertMu.Unlock()
	count := 0
	checkinAttempts.Range(func(key, value any) bool {
		if value.(*checkinAttemptState).day < state.day {
			checkinAttempts.CompareAndDelete(key, value)
		} else {
			count++
		}
		return true
	})
	if _, exists := checkinAttempts.Load(index); !exists && count >= maxCheckinAttemptEntries {
		return false
	}
	snapshot := state
	checkinAttempts.Store(index, &snapshot)
	return true
}

var maintenanceOptions struct {
	sync.RWMutex
	checkinAuto, tokenKeepalive bool
}

func configureMaintenance(checkinAuto, tokenKeepalive bool) {
	maintenanceOptions.Lock()
	maintenanceOptions.checkinAuto, maintenanceOptions.tokenKeepalive = checkinAuto, tokenKeepalive
	maintenanceOptions.Unlock()
	// No detached timers: turning auto off immediately prevents the next tick.
	// Keep claim uncertainty and budgets: toggling must not permit a second claim.
}
func maintenanceEnabled() (bool, bool) {
	maintenanceOptions.RLock()
	defer maintenanceOptions.RUnlock()
	return maintenanceOptions.checkinAuto, maintenanceOptions.tokenKeepalive
}

var checkinStatusCall = func(a *auth.Auth, did string) (*upstream.CheckinStatusResult, error) {
	return upstreamClient.CheckinStatus(a, did)
}
var checkinClaimCall = func(a *auth.Auth, did string) (*upstream.CheckinClaimResult, error) {
	return upstreamClient.CheckinClaim(a, did)
}
var checkinRefreshCall = refreshSnapshot

func checkinAccount(index string) map[string]any {
	unlock := accountOperationLock(index)
	defer unlock()
	return checkinAccountLocked(index, time.Now())
}
func checkinAccountAutomatic(index string) {
	unlock := accountOperationLock(index)
	defer unlock()
	auto, _ := maintenanceEnabled()
	if !auto {
		return
	}
	checkinAccountLocked(index, time.Now(), true)
}
func checkinAccountLocked(index string, now time.Time, automatic ...bool) map[string]any {
	result := map[string]any{"auth_index": index, "confirmed": false}
	s, err := freshAuthSnapshot(index)
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	result["uid"], result["nickname"] = s.Auth.UID, s.Auth.Nickname
	if s.File.Disabled {
		result["error"] = "account is manually disabled"
		return result
	}
	if isIntlStoredAuth(s.Stored) {
		result["error"] = "check-in is not supported for intl accounts"
		return result
	}
	if s.Auth.DeviceID == "" {
		result["error"] = "missing bound deviceId; re-login before check-in"
		return result
	}
	day := now.In(checkinDayLocation).Format("2006-01-02")
	st := checkinAttemptState{day: day}
	saved, checkpointErr := readCheckinCheckpoint(s.Raw)
	if checkpointErr != nil {
		result["error"] = checkpointErr.Error()
		return result
	}
	if saved.day > day {
		result["error"] = "persisted check-in day is in the future; repair required"
		return result
	}
	if saved.day == day {
		st = saved
	}
	if v, ok := checkinAttempts.Load(index); ok && v.(*checkinAttemptState).day == day {
		st = *v.(*checkinAttemptState)
	}
	if st.confirmed {
		result["confirmed"], result["already_checked_in"] = true, true
		return result
	}
	if st.pending {
		result["claim_unconfirmed"] = true
	}
	if st.attempts >= maxDailyCheckinAttempts {
		result["error"] = "daily per-account check-in budget exhausted"
		return result
	}
	if now.Before(st.next) {
		result["error"] = "check-in retry backoff"
		result["retry_at"] = st.next.Format(time.RFC3339)
		return result
	}
	st.attempts++
	st.next = now.Add(time.Duration(1<<uint(st.attempts-1)) * time.Minute)
	if !storeCheckinAttempt(index, st) {
		result["error"] = "check-in state capacity reached; no upstream request sent"
		return result
	}
	defer func() {
		storeCheckinAttempt(index, st)
		if err := saveCheckinCheckpoint(s, st); err != nil {
			result["persist_error"] = "check-in state was not saved"
		}
	}()
	if err := saveCheckinCheckpoint(s, st); err != nil {
		result["error"] = "cannot checkpoint check-in budget"
		return result
	}
	if _, err := checkinRefreshCall(s, 0); err != nil {
		result["error"] = "refresh: " + err.Error()
		return result
	}
	did := upstream.CheckinDeviceFor(s.Auth)
	observed := time.Now()
	status, err := checkinStatusCall(s.Auth, did)
	if err != nil || status == nil {
		result["error"] = fmt.Sprintf("checkin_status: %v", err)
		return result
	}
	confirmed := status.CheckedIn || status.DidCheckedIn
	result["already_checked_in"] = confirmed
	claimAccepted := false
	if !confirmed && !st.pending && status.Enable {
		// Mark uncertainty BEFORE the mutating request: transport errors can lose
		// the response after upstream has committed. Never blindly replay it.
		if len(automatic) > 0 && automatic[0] {
			auto, _ := maintenanceEnabled()
			if !auto {
				result["error"] = "automatic check-in disabled"
				return result
			}
		}
		st.pending = true
		if err := saveCheckinCheckpoint(s, st); err != nil {
			result["error"] = "cannot checkpoint pending claim"
			return result
		}
		claim, claimErr := checkinClaimCall(s.Auth, did)
		result["claim_unconfirmed"] = true
		if claimErr != nil {
			result["error"] = "checkin_claim: " + claimErr.Error()
			var rejection *upstream.Error
			if errors.As(claimErr, &rejection) && upstream.IsRateLimit9074(rejection.BizCode) {
				// Trae's explicit activity rejection is not an uncertain receipt.
				// It may retry within this account's remaining daily budget.
				st.pending = false
				delete(result, "claim_unconfirmed")
			}
		} else if claim != nil {
			result["claim_code"] = claim.Code
			claimAccepted = claim.Code == 0
			if claimAccepted {
				observed = time.Now()
			}
			if claim.ClaimCredits != nil {
				result["claim_credits"] = *claim.ClaimCredits
			}
		}
		// A claim receipt (even code 0) is not proof that check-in took effect.
		after, recheckErr := checkinStatusCall(s.Auth, did)
		if recheckErr != nil || after == nil {
			result["verify_error"] = fmt.Sprintf("checkin_status: %v", recheckErr)
		} else {
			status = after
			confirmed = after.CheckedIn || after.DidCheckedIn
		}
	}
	st.confirmed = confirmed
	if confirmed {
		st.pending = false
		st.next = time.Time{}
		delete(result, "claim_unconfirmed")
		delete(result, "error")
	}
	result["confirmed"] = confirmed
	result["checked_in"] = confirmed
	result["credits"], result["reward_credits"], result["extra_credits"] = status.Credits, status.Credits, status.ExtraCredits
	if !confirmed && !status.Enable && !st.pending {
		result["error"] = "check-in is not enabled"
	}
	if st.pending {
		result["claim_unconfirmed"] = true
	}
	updateCheckinCache(index, observed, claimAccepted, status)
	return result
}

// Daily check-in is 09:00 China time. Startup/re-enable never catch up a
// missed daily firing; only an attempt already started today may be retried.
func nextMaintenanceCheckin(now time.Time) time.Time {
	local := now.In(checkinDayLocation)
	next := time.Date(local.Year(), local.Month(), local.Day(), 9, 0, 0, 0, checkinDayLocation)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
func maintenanceCheckinDue(index string, now time.Time, daily bool) bool {
	if daily {
		return true
	}
	value, ok := checkinAttempts.Load(index)
	if !ok {
		return false
	}
	state := value.(*checkinAttemptState)
	return state.day == now.In(checkinDayLocation).Format("2006-01-02") && state.attempts > 0 && state.attempts < maxDailyCheckinAttempts && !state.confirmed && !now.Before(state.next)
}

// Maintenance enumerates host accounts, never accounts seen in parse/execute.
// A minute tick is only a local due check; each account gets a finite daily
// check-in budget and a bounded periodic token refresh, with the same lock.
func maintenanceRun(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var nextRefresh time.Time
	nextCheckin := nextMaintenanceCheckin(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			daily := !now.Before(nextCheckin)
			if daily {
				nextCheckin = nextMaintenanceCheckin(now)
			}
			// Model refresh is independent of check-in/token keepalive switches.
			refreshModelCatalogsDaily(ctx, now)
			auto, keepalive := maintenanceEnabled()
			files, err := maintenanceAuthList()
			if err != nil {
				continue
			}
			present := map[string]bool{}
			for _, f := range files {
				present[f.AuthIndex] = true
			}
			checkinAttempts.Range(func(k, v any) bool {
				if !present[k.(string)] {
					checkinAttempts.CompareAndDelete(k, v)
				}
				return true
			})
			accountCache.Range(func(k, v any) bool {
				if !present[k.(string)] {
					accountCache.CompareAndDelete(k, v)
				}
				return true
			})
			quotaObservations.Range(func(k, v any) bool {
				if !present[v.(quotaObservation).AuthIndex] {
					quotaObservations.CompareAndDelete(k, v)
				}
				return true
			})
			refreshDue := keepalive && !now.Before(nextRefresh)
			if refreshDue {
				nextRefresh = now.Add(time.Hour)
			}
			for _, f := range files {
				if ctx.Err() != nil {
					return
				}
				auto, keepalive = maintenanceEnabled()
				if f.Disabled {
					continue
				}
				if auto && maintenanceCheckinDue(f.AuthIndex, now, daily) {
					checkinAccountAutomatic(f.AuthIndex)
				}
				if refreshDue && keepalive {
					func() {
						unlock := accountOperationLock(f.AuthIndex)
						defer unlock()
						_, keepalive := maintenanceEnabled()
						if !keepalive {
							return
						}
						s, err := freshAuthSnapshot(f.AuthIndex)
						if err == nil {
							_, _ = refreshSnapshot(s, defaultRefreshSkew)
						}
					}()
				}
			}
		}
	}
}
