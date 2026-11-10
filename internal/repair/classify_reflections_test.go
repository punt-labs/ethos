//go:build linux || darwin

package repair

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// The reflections aggregate's class depends on record mode as well as on
// round_decisions: full mode publishes it, blame mode without decisions reads
// it as authority, blame mode with decisions leaves it as legacy detail.
func TestClassifyReflections(t *testing.T) {
	const withDecisions = "round_decisions: {version: 1, entries: []}\n"
	const legacyContract = "mission_id: m-one\n"
	for _, tc := range []struct {
		name     string
		mode     layout.RecordMode
		contract string
		export   bool
		want     TraceClass
	}{
		{"blame, decisions present", layout.RecordBlame, withDecisions, false, TraceLegacyDetail},
		{"blame, no decisions", layout.RecordBlame, legacyContract, false, TraceAuthority},
		{"full, decisions present", layout.RecordFull, withDecisions, false, TraceCurrentPublication},
		{"full, no decisions", layout.RecordFull, legacyContract, false, TraceCurrentPublication},
		{"blame, decisions present, exported", layout.RecordBlame, withDecisions, true, TraceCurrentPublication},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			p, err := layout.NewRepository(root, root, tc.mode)
			require.NoError(t, err)
			put(t, filepath.Join(p.RecordRoot(), "missions", "m-one", "contract.yaml"), tc.contract)
			put(t, filepath.Join(p.RecordRoot(), "missions", "m-one", "reflections.yaml"), "reflections: []\n")
			if tc.export {
				put(t, filepath.Join(p.RecordRoot(), "missions", "m-one.contract.yaml"), "export\n")
			}
			got, err := ClassifyReflections(p, p.RecordRoot(), "m-one")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// An unreadable or ambiguous contract cannot license any class.
func TestClassifyReflectionsAmbiguousAuthorityIsAnError(t *testing.T) {
	root := t.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	put(t, filepath.Join(p.RecordRoot(), "missions", "m-one", "contract.yaml"), "[broken")
	_, err = ClassifyReflections(p, p.RecordRoot(), "m-one")
	require.ErrorContains(t, err, "left in place")
}
