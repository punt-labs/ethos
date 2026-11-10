//go:build linux || darwin

package repair

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

const aggregateOneRound = `reflections:
  - mission: m-2026-11-09-001
    round: 1
    created_at: "2026-11-09T23:03:22Z"
    author: claude
    converging: true
    signals:
      - tests passing
    recommendation: continue
    reason: round 1 finished cleanly
`

func legacyIDForAggregate(t *testing.T) string {
	t.Helper()
	rs, err := mission.DecodeLegacyReflections([]byte(aggregateOneRound), "m-2026-11-09-001")
	require.NoError(t, err)
	require.Len(t, rs, 1)
	return mission.LegacyDecisionID(rs[0])
}

func contractWith(id string) string {
	return "mission_id: m-2026-11-09-001\nround_decisions:\n  version: 1\n  entries:\n    - id: " + id +
		"\n      round: 1\n      created_at: \"2026-11-09T23:03:22Z\"\n      author: claude\n      recommendation: continue\n"
}

// The aggregate's class comes from the aggregate and the contract, not from
// the current record mode: an id a live reflect allocated means the aggregate
// is that reflect's publication in either mode.
func TestClassifyReflections(t *testing.T) {
	legacyID := legacyIDForAggregate(t)
	const liveID = "bf96036b8393d71fc23fadc534e00376"
	for _, tc := range []struct {
		name     string
		mode     layout.RecordMode
		contract string
		export   bool
		want     TraceClass
	}{
		{"blame, imported decision", layout.RecordBlame, contractWith(legacyID), false, TraceLegacyDetail},
		{"full, imported decision", layout.RecordFull, contractWith(legacyID), false, TraceLegacyDetail},
		{"blame, live decision (full->blame switch on a clone)", layout.RecordBlame, contractWith(liveID), false, TraceCurrentPublication},
		{"full, live decision", layout.RecordFull, contractWith(liveID), false, TraceCurrentPublication},
		{"blame, no decisions key", layout.RecordBlame, "mission_id: m-2026-11-09-001\n", false, TraceAuthority},
		{"blame, decisions null", layout.RecordBlame, "mission_id: m-2026-11-09-001\nround_decisions: null\n", false, TraceAuthority},
		{"blame, imported decision, exported", layout.RecordBlame, contractWith(legacyID), true, TraceCurrentPublication},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			p, err := layout.NewRepository(root, root, tc.mode)
			require.NoError(t, err)
			put(t, filepath.Join(p.RecordRoot(), "missions", "m-2026-11-09-001", "contract.yaml"), tc.contract)
			put(t, filepath.Join(p.RecordRoot(), "missions", "m-2026-11-09-001", "reflections.yaml"), aggregateOneRound)
			if tc.export {
				put(t, filepath.Join(p.RecordRoot(), "missions", "m-2026-11-09-001.contract.yaml"), "export\n")
			}
			got, err := ClassifyReflections(p, p.RecordRoot(), "m-2026-11-09-001")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A round the contract never decided, beside decided ones, is not legacy
// detail either: nothing can say who wrote it.
func TestClassifyReflectionsUndecidedRoundIsNotLegacy(t *testing.T) {
	root := t.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	put(t, filepath.Join(p.RecordRoot(), "missions", "m-2026-11-09-001", "contract.yaml"), "mission_id: m-2026-11-09-001\nround_decisions: {version: 1, entries: []}\n")
	put(t, filepath.Join(p.RecordRoot(), "missions", "m-2026-11-09-001", "reflections.yaml"), aggregateOneRound)
	got, err := ClassifyReflections(p, p.RecordRoot(), "m-2026-11-09-001")
	require.NoError(t, err)
	assert.Equal(t, TraceCurrentPublication, got)
}

// An unreadable contract, or no contract in either root, cannot license any
// class.
func TestClassifyReflectionsNeedsAReadableContract(t *testing.T) {
	root := t.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	put(t, filepath.Join(p.RecordRoot(), "missions", "m-2026-11-09-001", "reflections.yaml"), aggregateOneRound)
	_, err = ClassifyReflections(p, p.RecordRoot(), "m-2026-11-09-001")
	require.ErrorContains(t, err, "no contract in either record root")

	put(t, filepath.Join(p.RecordRoot(), "missions", "m-2026-11-09-001", "contract.yaml"), "[broken")
	_, err = ClassifyReflections(p, p.RecordRoot(), "m-2026-11-09-001")
	require.ErrorContains(t, err, "left in place")
}
