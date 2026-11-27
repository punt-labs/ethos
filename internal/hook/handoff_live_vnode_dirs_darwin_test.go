package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDarwinLiveVnodesWatchEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	empty := filepath.Join(root, "empty")
	require.NoError(t, os.Mkdir(empty, 0o700))
	git, err := resolveDarwinLiveGit(t.Context(), root)
	require.NoError(t, err)
	guard, err := armDarwinLiveVnodes(t.Context(), root, git, nil, 128)
	require.NoError(t, err)
	defer guard.close()
	probe := filepath.Join(empty, "transient")
	require.NoError(t, os.WriteFile(probe, nil, 0o600))
	require.NoError(t, os.Remove(probe))
	require.ErrorContains(t, guard.drain(), empty)
}
