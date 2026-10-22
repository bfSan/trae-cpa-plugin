package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// A pending receipt is durable before the claim leaves this process. Restart,
// config toggles and a lost response must not turn verification into a replay.
type checkinCheckpoint struct {
	Day       string    `json:"day"`
	Attempts  int       `json:"attempts"`
	Pending   bool      `json:"pending"`
	Confirmed bool      `json:"confirmed"`
	Next      time.Time `json:"next,omitempty"`
}

func readCheckinCheckpoint(raw []byte) (checkinAttemptState, error) {
	invalid := func() (checkinAttemptState, error) {
		return checkinAttemptState{}, fmt.Errorf("invalid persisted check-in state; repair required before another claim")
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return invalid()
	}
	encoded, present := doc["checkin_state"]
	if !present {
		return checkinAttemptState{}, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || fields == nil {
		return invalid()
	}
	for _, key := range []string{"day", "attempts", "pending", "confirmed", "next"} {
		value, present := fields[key]
		if !present || string(value) == "null" {
			return invalid()
		}
	}
	var c checkinCheckpoint
	if json.Unmarshal(encoded, &c) != nil {
		return invalid()
	}
	day, err := time.ParseInLocation("2006-01-02", c.Day, checkinDayLocation)
	if err != nil || day.Format("2006-01-02") != c.Day || c.Attempts < 1 || c.Attempts > maxDailyCheckinAttempts || (c.Pending && c.Confirmed) {
		return invalid()
	}
	if c.Confirmed {
		if !c.Next.IsZero() {
			return invalid()
		}
	} else {
		// The largest attempt delay is eight minutes. A late-night attempt may
		// legitimately put Next just over midnight, never days/months away.
		end := day.AddDate(0, 0, 1).Add(time.Duration(1<<uint(maxDailyCheckinAttempts-1)) * time.Minute)
		if c.Next.IsZero() || c.Next.Before(day) || c.Next.After(end) {
			return invalid()
		}
	}
	return checkinAttemptState{day: c.Day, attempts: c.Attempts, pending: c.Pending, confirmed: c.Confirmed, next: c.Next}, nil
}
func saveCheckinCheckpoint(snapshot *authSnapshot, state checkinAttemptState) error {
	current, err := freshAuthSnapshot(snapshot.File.AuthIndex)
	if err != nil {
		return err
	}
	if current.File.Name != snapshot.File.Name || current.Auth.UID != snapshot.Auth.UID || current.Auth.Variant != snapshot.Auth.Variant {
		return fmt.Errorf("check-in account identity changed")
	}
	if current.File.Disabled {
		return fmt.Errorf("account is manually disabled")
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(current.Raw, &doc) != nil || doc == nil {
		return fmt.Errorf("invalid check-in credential document")
	}
	doc["checkin_state"], err = json.Marshal(checkinCheckpoint{Day: state.day, Attempts: state.attempts, Pending: state.pending, Confirmed: state.confirmed, Next: state.next})
	if err != nil {
		return err
	}
	if current.File.Disabled {
		doc["disabled"] = json.RawMessage("true")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return maintenanceAuthSave(current.File.Name, raw)
}
