package mission

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestStaleness covers the shapes the design was written to fix: a
// mission with no activity beyond creation, a reflect-only mission
// whose UpdatedAt never moves (the buried-timestamp gap Store.Abandon's
// own comment warns about), a mission with a result on record, and a
// caller that could not verify its delegation count.
func TestStaleness(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name             string
		created          string
		updated          string
		events           []Event
		results          []Result
		delegationCount  int
		delegationsKnown bool
		want             StalenessInfo
	}{
		{
			name:             "no events, no results — last activity is created_at",
			created:          "2026-08-01T00:00:00Z",
			updated:          "2026-08-01T00:00:00Z",
			delegationsKnown: true,
			want: StalenessInfo{
				LastActivityAt:   "2026-08-01T00:00:00Z",
				AgeDays:          5,
				AgeDaysKnown:     true,
				HasResults:       false,
				DelegationCount:  0,
				DelegationsKnown: true,
			},
		},
		{
			name:    "reflect-only mission — event ts wins over a stale updated_at",
			created: "2026-07-31T00:01:00Z",
			updated: "2026-07-31T00:01:00Z",
			events: []Event{
				{TS: "2026-07-31T00:01:00Z", Event: "create", Actor: "claude"},
				{TS: "2026-07-31T00:05:00Z", Event: "reflect", Actor: "rmh"},
			},
			delegationsKnown: true,
			want: StalenessInfo{
				LastActivityAt:   "2026-07-31T00:05:00Z",
				AgeDays:          6,
				AgeDaysKnown:     true,
				HasResults:       false,
				DelegationCount:  0,
				DelegationsKnown: true,
			},
		},
		{
			name:    "results present — HasResults true",
			created: "2026-08-06T00:00:00Z",
			updated: "2026-08-06T06:00:00Z",
			results: []Result{
				{Mission: "m-2026-08-05-001", Round: 1, Verdict: VerdictPass},
			},
			delegationCount:  1,
			delegationsKnown: true,
			want: StalenessInfo{
				LastActivityAt:   "2026-08-06T06:00:00Z",
				AgeDays:          0,
				AgeDaysKnown:     true,
				HasResults:       true,
				DelegationCount:  1,
				DelegationsKnown: true,
			},
		},
		{
			name:             "delegations unknown is preserved, not collapsed to zero",
			created:          "2026-08-06T00:00:00Z",
			updated:          "2026-08-06T00:00:00Z",
			delegationCount:  0,
			delegationsKnown: false,
			want: StalenessInfo{
				LastActivityAt:   "2026-08-06T00:00:00Z",
				AgeDays:          0,
				AgeDaysKnown:     true,
				HasResults:       false,
				DelegationCount:  0,
				DelegationsKnown: false,
			},
		},
		{
			name:    "out-of-order event log — max, not last, wins",
			created: "2026-08-01T00:00:00Z",
			updated: "2026-08-01T00:00:00Z",
			events: []Event{
				{TS: "2026-08-04T00:00:00Z", Event: "reflect", Actor: "rmh"},
				{TS: "2026-08-02T00:00:00Z", Event: "update", Actor: "claude"},
			},
			delegationsKnown: true,
			want: StalenessInfo{
				LastActivityAt:   "2026-08-04T00:00:00Z",
				AgeDays:          2,
				AgeDaysKnown:     true,
				HasResults:       false,
				DelegationCount:  0,
				DelegationsKnown: true,
			},
		},
		{
			name:             "all timestamps garbage — AgeDaysKnown false, not AgeDays 0",
			created:          "not-a-time",
			updated:          "also-not-a-time",
			delegationsKnown: true,
			want: StalenessInfo{
				LastActivityAt:   "not-a-time",
				AgeDays:          0,
				AgeDaysKnown:     false,
				HasResults:       false,
				DelegationCount:  0,
				DelegationsKnown: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Contract{CreatedAt: tt.created, UpdatedAt: tt.updated}
			got := Staleness(c, tt.events, tt.results, tt.delegationCount, tt.delegationsKnown, now)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestLatestTimestamp exercises the fold helper directly: an
// unparseable or empty candidate must lose to a parseable one, and
// two unparseable candidates must not swap — the running max returned
// unchanged.
func TestLatestTimestamp(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want string
	}{
		{"both empty", "", "", ""},
		{"a empty, b valid", "", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z"},
		{"a valid, b empty", "2026-08-01T00:00:00Z", "", "2026-08-01T00:00:00Z"},
		{"b later", "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z", "2026-08-02T00:00:00Z"},
		{"a later", "2026-08-02T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"},
		{"b garbage, a valid", "2026-08-01T00:00:00Z", "not-a-timestamp", "2026-08-01T00:00:00Z"},
		{"a garbage, b valid", "not-a-timestamp", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z"},
		{"both garbage", "not-a-timestamp", "also-garbage", "not-a-timestamp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, latestTimestamp(tt.a, tt.b))
		})
	}
}
