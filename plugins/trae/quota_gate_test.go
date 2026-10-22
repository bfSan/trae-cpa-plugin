package main

import (
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"testing"
	"time"
)

func TestQuotaObservationScopeAndRelease(t *testing.T) {
	defer releaseQuotaObservation("a")
	observeChatFailure("a", "m1", upstream.ErrPlanLimit)
	if !quotaGuard("a", "m1") {
		t.Fatal("explicit upstream quota rejection must guard matching model")
	}
	if quotaGuard("b", "m1") || quotaGuard("a", "m2") {
		t.Fatal("observation crossed account/model boundary")
	}
	releaseQuotaObservation("a")
	if quotaGuard("a", "m1") {
		t.Fatal("release did not clear observation")
	}
}
func TestQuotaObservationExpiresNotMidnight(t *testing.T) {
	observeChatFailure("expiry", "m", upstream.ErrPlanLimit)
	key := quotaObservationKey("expiry", "m")
	value, _ := quotaObservations.Load(key)
	q := value.(quotaObservation)
	if q.Until.Sub(q.ObservedAt) != quotaObservationTTL {
		t.Fatal("must use evidence TTL, not a guessed reset")
	}
	q.Until = time.Now().Add(-time.Second)
	quotaObservations.Store(key, q)
	if quotaGuard("expiry", "m") {
		t.Fatal("expired observation should not guard")
	}
}
func TestQuotaZeroBalanceIsObservationOnly(t *testing.T) {
	sum := upstream.UsageSummary{RemainKnown: true, Remain: 0}
	updateUsageCache("zero", time.Now(), sum, "Free", nil, -1, false, false)
	defer accountCache.Delete("zero")
	if quotaGuard("zero", "") {
		t.Fatal("zero balance alone must not disable or guard")
	}
}
func TestPositiveUsageDoesNotClearUnrelatedQuota(t *testing.T) {
	observeChatFailure("positive", "m", upstream.ErrPlanLimit)
	defer releaseQuotaObservation("positive")
	defer accountCache.Delete("positive")
	updateUsageCache("positive", time.Now(), upstream.UsageSummary{RemainKnown: true, Remain: 100}, "Paid", nil, -1, false, false)
	if !quotaGuard("positive", "m") {
		t.Fatal("positive balance cannot prove model quota recovered")
	}
}
