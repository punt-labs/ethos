package layout

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryDestinationSyncsOwnedAncestors(t *testing.T) {
	store, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	p, err := NewRepository(store, checkout, RecordBlame)
	require.NoError(t, err)
	for _, root := range []string{store, checkout} {
		dir := filepath.Join(root, "new", "nested")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		want := []string{dir, filepath.Dir(dir), root}
		for fail := -1; fail < len(want); fail++ {
			var calls []string
			injected := errors.New("directory sync failed")
			err := syncRepositoryDestination(p, filepath.Join(dir, "file"), func(d string) error {
				calls = append(calls, d)
				if len(calls)-1 == fail {
					return injected
				}
				return nil
			})
			if fail < 0 {
				require.NoError(t, err)
				assert.Equal(t, want, calls)
			} else {
				require.ErrorIs(t, err, injected)
				assert.Equal(t, want[:fail+1], calls)
			}
		}
		// A retry must sync the same full chain, even though the directories
		// already existed when the attempt began.
		require.NoError(t, SyncRepositoryDestination(p, filepath.Join(dir, "file")))
	}
}

func TestRepositoryDestinationSyncsPhysicalExternalAncestors(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	p, err := NewRepository(root, root, RecordBlame)
	require.NoError(t, err)
	external, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(root, "host")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var calls []string
	require.NoError(t, syncRepositoryDestination(p, filepath.Join(link, "CLAUDE.md"), func(d string) error {
		calls = append(calls, d)
		return nil
	}))
	var want []string
	for d := external; ; d = filepath.Dir(d) {
		want = append(want, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	assert.Equal(t, want, calls)
}

func TestRepositoryDestinationSyncRejectsInvalidPolicy(t *testing.T) {
	err := syncRepositoryDestination(Policy{}, "file", func(string) error {
		t.Fatal("invalid policy reached directory sync")
		return nil
	})
	var invalid *ValidationError
	require.ErrorAs(t, err, &invalid)
}
