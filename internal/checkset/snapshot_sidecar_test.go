package checkset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshotSidecarFollowsWinningCheckSet(t *testing.T) {
	repo, global := t.TempDir(), t.TempDir()
	base := []byte("name: project\nchecks: [{name: full-gate, stage: handoff, files: all, adapter: make, target: check}]\n")
	sidecar := []byte(`checks:
  full-gate:
    profile: linux-worktree-v1
    epoch: test
    sources: {mode: worktree}
    runtime:
      platforms:
        linux-amd64:
          bundle: test-linux
          sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
        darwin-arm64:
          bundle: test-darwin
          sha256: "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0"
          url: https://example.com/darwin.tar.gz
`)
	for _, root := range []string{repo, global} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "checks", "snapshots"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "checks", "project.yaml"), base, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(global, "checks", "snapshots", "project.yaml"), sidecar, 0o600))
	store := NewLayeredStoreWithBundle(repo, "", global, false)
	set, err := store.LoadForHandoff("project")
	require.NoError(t, err)
	require.Nil(t, set.Checks[0].WorktreeSnapshot)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "checks", "snapshots", "project.yaml"), sidecar, 0o600))
	set, err = store.LoadForHandoff("project")
	require.NoError(t, err)
	require.False(t, set.Checks[0].InvalidSnapshotSidecar)
	require.NotNil(t, set.Checks[0].WorktreeSnapshot)
	require.Equal(t, "test-darwin", set.Checks[0].WorktreeSnapshot.Runtime.Platforms["darwin-arm64"].Bundle)
	require.Equal(t, "https://example.com/darwin.tar.gz", set.Checks[0].WorktreeSnapshot.Runtime.Platforms["darwin-arm64"].URL)
	argv, err := set.Checks[0].Argv()
	require.NoError(t, err)
	require.Equal(t, []string{"make", "check"}, argv)
	set, err = store.Load("project")
	require.NoError(t, err)
	require.Nil(t, set.Checks[0].WorktreeSnapshot)
	names, err := store.List()
	require.NoError(t, err)
	require.Equal(t, []string{"project"}, names)
}
