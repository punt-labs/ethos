package mission

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidStatusFilter(t *testing.T) {
	tests := []struct {
		filter string
		want   bool
	}{
		{StatusOpen, true},
		{StatusClosed, true},
		{StatusFailed, true},
		{StatusEscalated, true},
		{"all", true},
		{"", false},
		{"bogus", false},
		{"OPEN", false}, // case-sensitive
		{"in_progress", false},
	}
	for _, tt := range tests {
		t.Run(tt.filter+"="+boolStr(tt.want), func(t *testing.T) {
			assert.Equal(t, tt.want, IsValidStatusFilter(tt.filter))
		})
	}
}

func boolStr(b bool) string {
	if b {
		return "valid"
	}
	return "invalid"
}

func TestTopoSortContracts(t *testing.T) {
	// 3-stage pipeline: design -> implement -> test
	design := &Contract{MissionID: "m-2026-04-13-001", Pipeline: "p1"}
	implement := &Contract{MissionID: "m-2026-04-13-002", Pipeline: "p1",
		DependsOn: []string{"m-2026-04-13-001"}}
	test := &Contract{MissionID: "m-2026-04-13-003", Pipeline: "p1",
		DependsOn: []string{"m-2026-04-13-002"}}

	t.Run("already sorted", func(t *testing.T) {
		sorted, warns := TopoSortContracts([]*Contract{design, implement, test})
		assert.Empty(t, warns)
		assert.Equal(t, "m-2026-04-13-001", sorted[0].MissionID)
		assert.Equal(t, "m-2026-04-13-002", sorted[1].MissionID)
		assert.Equal(t, "m-2026-04-13-003", sorted[2].MissionID)
	})

	t.Run("reversed input", func(t *testing.T) {
		sorted, warns := TopoSortContracts([]*Contract{test, implement, design})
		assert.Empty(t, warns)
		assert.Equal(t, "m-2026-04-13-001", sorted[0].MissionID)
		assert.Equal(t, "m-2026-04-13-002", sorted[1].MissionID)
		assert.Equal(t, "m-2026-04-13-003", sorted[2].MissionID)
	})

	t.Run("single element", func(t *testing.T) {
		sorted, warns := TopoSortContracts([]*Contract{design})
		assert.Empty(t, warns)
		assert.Len(t, sorted, 1)
	})

	t.Run("missing dependency outside set", func(t *testing.T) {
		orphan := &Contract{MissionID: "m-2026-04-13-010",
			DependsOn: []string{"m-2026-04-13-099"}} // not in set
		sorted, warns := TopoSortContracts([]*Contract{orphan})
		assert.Empty(t, warns) // missing deps outside the set are ignored
		assert.Len(t, sorted, 1)
	})

	t.Run("no dependencies", func(t *testing.T) {
		a := &Contract{MissionID: "m-2026-04-13-020"}
		b := &Contract{MissionID: "m-2026-04-13-021"}
		sorted, warns := TopoSortContracts([]*Contract{a, b})
		assert.Empty(t, warns)
		assert.Len(t, sorted, 2)
	})
}

func TestStatusMatches(t *testing.T) {
	tests := []struct {
		name           string
		filter         string
		contractStatus string
		want           bool
	}{
		{"empty filter matches open", "", StatusOpen, true},
		{"empty filter matches closed", "", StatusClosed, true},
		{"all matches open", "all", StatusOpen, true},
		{"all matches closed", "all", StatusClosed, true},
		{"all matches failed", "all", StatusFailed, true},
		{"all matches escalated", "all", StatusEscalated, true},
		{"open matches open", StatusOpen, StatusOpen, true},
		{"open does not match closed", StatusOpen, StatusClosed, false},
		{"closed matches closed", StatusClosed, StatusClosed, true},
		{"closed does not match open", StatusClosed, StatusOpen, false},
		{"failed matches failed", StatusFailed, StatusFailed, true},
		{"escalated matches escalated", StatusEscalated, StatusEscalated, true},
		{"unknown filter matches nothing", "bogus", StatusOpen, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StatusMatches(tt.filter, tt.contractStatus))
		})
	}
}
