//go:build linux || darwin

package resolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingGit makes PATH hold a git whose rev-parse fails with stderr.
func failingGit(t *testing.T, stderr string) {
	t.Helper()
	fakeGit(t, fmt.Sprintf(`[ "$1" = --version ] && { echo "git version 2.50.0"; exit 0; }
echo %q >&2
exit 128
`, stderr))
}

// A FIFO .git is refused as a .git git does not accept, whichever way the git
// that runs words it: older gits say "not a git repository", newer ones "not a
// regular file".
func TestFIFODotGitRefusedInAnyGitWording(t *testing.T) {
	for _, msg := range []string{
		"fatal: not a git repository (or any of the parent directories): .git",
		"fatal: not a regular file: 'DIR/.git'",
	} {
		t.Run(msg, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, syscall.Mkfifo(filepath.Join(dir, ".git"), 0o644))
			failingGit(t, strings.ReplaceAll(msg, "DIR", dir))
			t.Chdir(dir)
			_, err := locateRepoRoot()
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrNotInRepo)
			assert.Contains(t, err.Error(), "exists but git does not accept it as a repository")
		})
	}
}

// A gitfile naming a gitdir that is gone is refused with that gitdir named,
// whether git names it ("not a git repository: <gitdir>") or not (newer gits
// print "(null)").
func TestStaleGitfileNamesTheGitdirInAnyGitWording(t *testing.T) {
	for _, form := range []string{
		"fatal: not a git repository: (null)",
		"fatal: not a git repository: GITDIR",
	} {
		t.Run(form, func(t *testing.T) {
			dir := t.TempDir()
			gitdir := filepath.Join(t.TempDir(), "gone", "worktrees", "wt")
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))
			failingGit(t, strings.ReplaceAll(form, "GITDIR", gitdir))
			t.Chdir(dir)
			_, err := locateRepoRoot()
			require.Error(t, err)
			assert.Contains(t, err.Error(), gitdir)
		})
	}
}
