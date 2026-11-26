package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDarwinLiveVnodesWatchSymlinkItself(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "target"), "unchanged")
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink("target", link))
	initGitRepo(t, root)
	stageGit(t, root, "add", "link")
	git, err := resolveDarwinLiveGit(t.Context(), root)
	require.NoError(t, err)
	guard, err := armDarwinLiveVnodes(t.Context(), root, git, []string{"link"}, 128)
	require.NoError(t, err)
	defer guard.close()
	require.NoError(t, guard.drain())
	require.NoError(t, os.Remove(link))
	require.ErrorContains(t, guard.drain(), "vnode")
}
