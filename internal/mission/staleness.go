package mission

import "time"

// StalenessInfo summarizes how long a mission has gone without
// activity, for display and for --stale filtering. It is a read-only
// projection: nothing here mutates a contract or triggers an action.
type StalenessInfo struct {
	LastActivityAt   string // RFC3339; max(created_at, updated_at, latest event ts)
	AgeDays          int    // days since LastActivityAt, floor; meaningless when AgeDaysKnown is false
	AgeDaysKnown     bool   // false when LastActivityAt did not parse as RFC3339
	HasResults       bool   // len(results) > 0, any round
	DelegationCount  int    // entries under delegations/, best-effort
	DelegationsKnown bool   // false when repoRoot is unset and the count could not be checked
}

// Staleness computes StalenessInfo for a mission from data the caller
// already has: the contract, its event log, its results, and a
// delegation count. It is a pure function — no store, no disk access,
// no side effect — so a caller without a repoRoot (a bare Store in a
// unit test, or a future cross-repo query) can still get an answer,
// and callers with different data sources (live union log vs. legacy
// tracked log) share one calculation.
//
// LastActivityAt is the max of CreatedAt, UpdatedAt, and every event's
// TS. AppendReflection never touches UpdatedAt (only Update and a
// terminal transition do), so a mission that reflected once and then
// went dark would otherwise read as active-as-of-creation; scanning
// the event log closes that gap.
//
// delegationsKnown must be false, not delegationCount silently zero,
// whenever the caller could not verify the count — the same
// fail-closed posture Store.Abandon's gate 1 uses for the identical
// data (internal/mission/store.go), applied here to a read-only
// signal instead of a mutation gate. A caller must never let
// "unknown" collapse into "zero", which a leader could misread as
// "definitely no delegations" rather than "not checked."
func Staleness(c *Contract, events []Event, results []Result, delegationCount int, delegationsKnown bool, now time.Time) StalenessInfo {
	last := latestTimestamp(c.CreatedAt, c.UpdatedAt)
	for _, e := range events {
		last = latestTimestamp(last, e.TS)
	}

	info := StalenessInfo{
		LastActivityAt:   last,
		HasResults:       len(results) > 0,
		DelegationCount:  delegationCount,
		DelegationsKnown: delegationsKnown,
	}
	if t, err := time.Parse(time.RFC3339, last); err == nil {
		days := int(now.Sub(t).Hours() / 24)
		if days < 0 {
			// Clock skew or a hand-edited future timestamp -- clamp
			// rather than report a negative "days since last activity",
			// which would read as nonsense and could leak into a future
			// --stale-days filter as a false "very fresh" signal.
			days = 0
		}
		info.AgeDays = days
		info.AgeDaysKnown = true
	}
	return info
}

// latestTimestamp returns whichever of a, b is the later RFC3339
// timestamp. An unparseable or empty candidate loses to a parseable
// one; if neither parses, a is returned unchanged so a caller folding
// this over a list never has its running max reset to garbage by one
// bad entry.
func latestTimestamp(a, b string) string {
	bt, err := time.Parse(time.RFC3339, b)
	if err != nil {
		return a
	}
	at, err := time.Parse(time.RFC3339, a)
	if err != nil {
		return b
	}
	if bt.After(at) {
		return b
	}
	return a
}
