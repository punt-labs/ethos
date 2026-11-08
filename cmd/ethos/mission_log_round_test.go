//go:build linux || darwin

package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestDetailRoundExactNumbers(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
		want  float64
	}{
		{"repository integer", json.Number("2"), 2},
		{"standalone float", float64(2), 2},
		{"in-process integer", int(2), 2},
		{"precise fraction above one", json.Number("1.0000000000000001"), 0},
		{"precise fraction below one", json.Number("0.99999999999999999"), 0},
		{"decimal syntax", json.Number("1.0"), 0},
		{"exponent syntax", json.Number("1e0"), 0},
		{"integer overflow", json.Number("9223372036854775808"), 0},
		{"malformed number", json.Number("not-a-number"), 0},
		{"string", "2", 0},
		{"null", nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, detailRound(map[string]any{"round": tt.value}, "round"))
		})
	}
}

func TestMissionLogHumanRepositoryRounds(t *testing.T) {
	se, s, id := fullExportFixture(t)
	round, err := s.AdvanceRound(id, "claude")
	require.NoError(t, err)
	require.Equal(t, 2, round)
	require.NoError(t, s.AppendResult(id, &mission.Result{
		Mission: id, Round: 2, Author: "bwk", Verdict: mission.VerdictPass, Confidence: 0.8,
		Evidence: []mission.EvidenceCheck{{Name: "round display check", Status: mission.EvidenceStatusPass}},
	}))
	_, err = s.Close(id, mission.StatusClosed)
	require.NoError(t, err)
	require.NoError(t, s.Correct(id, mission.Correction{
		Mission: id, Round: 2, Author: "claude", Kind: mission.CorrectionFactual,
		Claim: "original round-two claim", Corrected: "accepted round-two annotation",
	}))
	p, ok := s.RepositoryPolicy()
	require.True(t, ok)
	stream := audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "export-session"}
	pending, err := audit.UnarchivedCount(p, stream)
	require.NoError(t, err)
	require.Positive(t, pending, "the first CLI reads must exercise the live tail")
	for _, state := range []string{"live", "sealed"} {
		t.Run(state, func(t *testing.T) {
			if state == "sealed" {
				_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "round-log-export")
				require.Equal(t, 0, exit, stderr)
				pending, err := audit.UnarchivedCount(p, stream)
				require.NoError(t, err)
				require.Zero(t, pending, "the second CLI reads must exercise sealed history")
			}
			for _, tt := range []struct {
				kind string
				want string
			}{
				{"reflect", "round=1 rec=continue"},
				{"result", "round=2 verdict=pass"},
				{"round_advanced", "round 1 -> 2"},
				{mission.EventCorrect, "kind=factual round=2"},
				{"close", "status=closed verdict=pass round=2"},
			} {
				t.Run(tt.kind, func(t *testing.T) {
					stdout, stderr, exit := runCLI(t, se, "mission", "log", id, "--event", tt.kind)
					require.Equal(t, 0, exit, stderr)
					assert.Contains(t, stdout, tt.want)
				})
			}
		})
	}
}
