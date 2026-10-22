package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bfSan/trae-cpa-plugin/plugins/trae/auth"
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
)

func TestMaintenanceCheckpointMalformedFailsClosed(t *testing.T) {
	cases := []string{
		`null`, `[]`, `"not-a-record"`, `{}`,
		`{"day":"2026-10-22","attempts":-1,"pending":true,"confirmed":false,"next":"2026-10-22T12:01:00+08:00"}`,
		`{"day":"2026-10-22","attempts":5,"pending":true,"confirmed":false,"next":"2026-10-22T12:01:00+08:00"}`,
		`{"day":"2026-10-22","attempts":null,"pending":true,"confirmed":false,"next":"2026-10-22T12:01:00+08:00"}`,
		`{"day":"2026-10-22","attempts":1,"pending":true,"confirmed":false,"next":"broken"}`,
		`{"day":"2026-10-22","attempts":1,"pending":true,"confirmed":false,"next":"2030-01-01T00:00:00Z"}`,
		`{"day":"2026-10-22","attempts":1,"pending":true,"confirmed":false,"next":"2026-10-21T12:00:00+08:00"}`,
		`{"day":"2026-10-22","attempts":1,"pending":true,"confirmed":true,"next":"0001-01-01T00:00:00Z"}`,
		`{"day":"bad-day","attempts":1,"pending":true,"confirmed":false,"next":"2026-10-22T12:01:00+08:00"}`,
	}
	for i, encoded := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f := mockMaintenance(t)
			var doc map[string]json.RawMessage
			_ = json.Unmarshal(f.doc, &doc)
			doc["checkin_state"] = json.RawMessage(encoded)
			f.doc, _ = json.Marshal(doc)
			checkinStatusCall = func(*auth.Auth, string) (*upstream.CheckinStatusResult, error) {
				t.Fatal("invalid checkpoint contacted upstream")
				return nil, nil
			}
			result := checkinAccountLocked("idx", time.Date(2026, 10, 22, 12, 0, 0, 0, checkinDayLocation))
			if result["confirmed"] != false || result["error"] == nil || f.saves != 0 {
				t.Fatalf("invalid checkpoint was reset: %+v saves=%d", result, f.saves)
			}
		})
	}
	if _, err := readCheckinCheckpoint([]byte(`{"auth":{}}`)); err != nil {
		t.Fatalf("absent checkpoint must be fresh: %v", err)
	}
}

func isolateMaintenanceMap(t *testing.T, m *sync.Map) {
	t.Helper()
	type pair struct{ key, value any }
	saved := []pair{}
	m.Range(func(k, v any) bool { saved = append(saved, pair{k, v}); m.Delete(k); return true })
	t.Cleanup(func() {
		m.Range(func(k, v any) bool { m.Delete(k); return true })
		for _, p := range saved {
			m.Store(p.key, p.value)
		}
	})
}

func TestMaintenanceQuotaObservationBoundKeepsLiveEvidence(t *testing.T) {
	isolateMaintenanceMap(t, &quotaObservations)
	now := time.Now()
	for i := 0; i < maxQuotaObservations; i++ {
		q := quotaObservation{AuthIndex: "bound", Model: fmt.Sprint(i), ObservedAt: now, Until: now.Add(time.Hour)}
		quotaObservations.Store(quotaObservationKey(q.AuthIndex, q.Model), q)
	}
	overflow := quotaObservation{AuthIndex: "bound", Model: "overflow", ObservedAt: now, Until: now.Add(time.Hour)}
	if storeQuotaObservation(overflow) {
		t.Fatal("new model admitted above cap")
	}
	if !quotaGuard("bound", "0") {
		t.Fatal("live evidence evicted to admit new model")
	}
	updated := overflow
	updated.Model = "0"
	if !storeQuotaObservation(updated) {
		t.Fatal("existing observation cannot update at cap")
	}
	expired := updated
	expired.Model = "1"
	expired.Until = now.Add(-time.Second)
	quotaObservations.Store(quotaObservationKey("bound", "1"), expired)
	if !storeQuotaObservation(overflow) {
		t.Fatal("expired observation did not free capacity")
	}
	if _, ok := quotaObservations.Load(quotaObservationKey("bound", "1")); ok {
		t.Fatal("expired evidence retained")
	}
	count := 0
	quotaObservations.Range(func(_, _ any) bool { count++; return true })
	if count != maxQuotaObservations {
		t.Fatalf("bound violated: %d", count)
	}
}

func TestMaintenanceCheckinAttemptBoundKeepsPending(t *testing.T) {
	isolateMaintenanceMap(t, &checkinAttempts)
	state := checkinAttemptState{day: "2026-10-22", attempts: 1, pending: true}
	for i := 0; i < maxCheckinAttemptEntries; i++ {
		snapshot := state
		checkinAttempts.Store(fmt.Sprint(i), &snapshot)
	}
	if storeCheckinAttempt("overflow", state) {
		t.Fatal("new account state admitted above cap")
	}
	value, _ := checkinAttempts.Load("0")
	if !value.(*checkinAttemptState).pending {
		t.Fatal("pending receipt evicted")
	}
	if !storeCheckinAttempt("0", state) {
		t.Fatal("existing pending state cannot update")
	}
	old := state
	old.day = "2026-10-21"
	checkinAttempts.Store("1", &old)
	if !storeCheckinAttempt("overflow", state) {
		t.Fatal("prior-day entry did not free capacity")
	}
	count := 0
	checkinAttempts.Range(func(_, _ any) bool { count++; return true })
	if count != maxCheckinAttemptEntries {
		t.Fatalf("checkin bound violated: %d", count)
	}
}
