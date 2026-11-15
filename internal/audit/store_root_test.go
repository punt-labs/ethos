package audit

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
}

// A linked worktree of a bare repository is its own store: there is no work
// tree to hold one.
func TestStoreRootOf_BareRepositoryWorktreeIsOwnStore(t *testing.T) {
	d := t.TempDir()
	gitDir := filepath.Join(d, ".git")
	runGitT(t, d, "init", "-q", "--bare", gitDir)
	out, err := exec.Command("git", "-C", gitDir, "-c", "user.email=a@b", "-c", "user.name=a",
		"commit-tree", "-m", "x", "4b825dc642cb6eb9a060e54bf8d69288fbee4904").Output()
	require.NoError(t, err)
	runGitT(t, gitDir, "worktree", "add", "--detach", filepath.Join(d, "wt"), strings.TrimSpace(string(out)))
	wt := filepath.Join(d, "wt")
	got, err := StoreRootOf(wt)
	require.NoError(t, err)
	assert.Equal(t, wt, got)
}
