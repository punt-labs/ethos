package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/stretchr/testify/require"
)

func TestDarwinSnapshotRejectsHomeSystemInputs(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	outside, err := os.MkdirTemp(home, "ethos-fake-host-secret-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	secret := filepath.Join(outside, "fixture")
	writeFile(t, secret, "synthetic secret")
	root := t.TempDir()
	for _, input := range []checkset.SnapshotSystemInputs{
		{Files: []string{secret}},
		{Dirs: []string{outside}},
	} {
		profile := &checkset.WorktreeSnapshot{
			System: checkset.SnapshotSystem{SnapshotSystemInputs: input},
		}
		_, err := prepareHandoffSystem(t.Context(), root, profile)
		require.ErrorContains(t, err, "outside supported Darwin system roots")
	}
}
