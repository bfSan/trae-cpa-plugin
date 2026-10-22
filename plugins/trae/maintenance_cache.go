package main

import (
	"github.com/bfSan/trae-cpa-plugin/plugins/trae/upstream"
	"time"
)

// Entries are immutable. CAS merges independent fields without allowing a
// slow pre-claim usage response to overwrite newer observations/invalidation.
func mutateAccountCache(index string, change func(*accountCacheEntry)) {
	for {
		value, loaded := accountCache.Load(index)
		next := &accountCacheEntry{credits: -1}
		if loaded {
			*next = *value.(*accountCacheEntry)
		}
		change(next)
		if loaded {
			if accountCache.CompareAndSwap(index, value, next) {
				return
			}
		} else {
			if _, raced := accountCache.LoadOrStore(index, next); !raced {
				return
			}
		}
	}
}
func updateCheckinCache(index string, observed time.Time, accepted bool, status *upstream.CheckinStatusResult) {
	mutateAccountCache(index, func(e *accountCacheEntry) {
		if !observed.Before(e.checkinObserved) {
			e.checkin = &checkinStatus{CheckedIn: status.CheckedIn || status.DidCheckedIn, Credits: status.Credits, Enable: status.Enable}
			e.checkinObserved = observed
		}
		if accepted && !observed.Before(e.usageObserved) {
			e.credits = -1
			e.usageFilled = false
			e.usageObserved = observed
		}
	})
}
func updateUsageCache(index string, observed time.Time, sum upstream.UsageSummary, plan string, bind *bindStatus, reward int64, checkedIn, enable bool) {
	mutateAccountCache(index, func(e *accountCacheEntry) {
		if !observed.Before(e.usageObserved) {
			e.usage = sum
			e.usageFilled = true
			e.plan = plan
			e.fetched = observed
			e.usageObserved = observed
			e.credits = -1
			if sum.RemainKnown {
				e.credits = sum.Remain
			}
		}
		if bind != nil && bind.Known && !observed.Before(e.bindObserved) {
			e.bind = bind
			e.bindObserved = observed
		}
		if reward >= 0 && !observed.Before(e.checkinObserved) {
			e.checkin = &checkinStatus{CheckedIn: checkedIn, Credits: reward, Enable: enable}
			e.checkinObserved = observed
		}
	})
}
