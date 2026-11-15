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

// A linked worktree whose common dir is neither bare nor a work tree's .git
// (a separate git dir) has no store ethos can name: resolve refuses it, and so
// must StoreRootOf, so a caller keeps what it cannot place.
func TestStoreRootOf_SeparateGitDirWorktreeIsRefused(t *testing.T) {
	d := t.TempDir()
	work, sep := filepath.Join(d, "work"), filepath.Join(d, "sep")
	runGitT(t, d, "init", "-q", "--separate-git-dir", sep, work)
	runGitT(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(d, "wt")
	runGitT(t, work, "worktree", "add", "-q", "--detach", wt)
	if got, err := StoreRootOf(wt); err == nil {
		t.Errorf("StoreRootOf = %q, nil; want a refusal", got)
	}
}
