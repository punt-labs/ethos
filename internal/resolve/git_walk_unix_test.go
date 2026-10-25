//go:build linux || darwin

package resolve

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	// git skips a .git that is neither a directory nor a regular file, and
	// opening a FIFO would block the walk forever.
	gitShapes["fifo"] = gitShape{func(t *testing.T, inner string) {
		t.Helper()
		require.NoError(t, syscall.Mkfifo(filepath.Join(inner, ".git"), 0o644))
	}, handOuter}

	// An unreadable .git directory: git skips it and resolves the enclosing
	// repository; the hand rules are deliberately stricter than git and refuse.
	gitShapes["mode-000 .git directory"] = gitShape{func(t *testing.T, inner string) {
		t.Helper()
		makeGitDirAt(t, filepath.Join(inner, ".git"))
		chmodUnreadable(t, filepath.Join(inner, ".git"), 0o755)
	}, handError}

	// An unreadable gitfile: git refuses it, and so do the hand rules.
	gitShapes["mode-000 gitfile"] = gitShape{func(t *testing.T, inner string) {
		t.Helper()
		gitfileTo(makeGitDirAt, "gitdir: ")(t, inner)
		chmodUnreadable(t, filepath.Join(inner, ".git"), 0o644)
	}, handError}

	// A linked worktree whose commondir cannot be read: git refuses it, and so
	// do the hand rules, rather than fall back to the worktree's own store.
	gitShapes["linked worktree with unreadable commondir"] = gitShape{func(t *testing.T, inner string) {
		t.Helper()
		skipIfRoot(t)
		linkedWorktree(0o000)(t, inner)
	}, handError}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
}

// chmodUnreadable makes p mode 000 for the test, restoring mode after.
func chmodUnreadable(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	skipIfRoot(t)
	require.NoError(t, os.Chmod(p, 0o000))
	t.Cleanup(func() { _ = os.Chmod(p, mode) })
}

// An unreadable .git directory follows git when git is on PATH (git skips it,
// so the enclosing repository resolves) and is refused by the hand rules,
// which are deliberately stricter than git there.
func TestUnreadableGitDirectory(t *testing.T) {
	t.Run("git", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		inner, outer := shapeIn(t, gitShapes["mode-000 .git directory"])
		want, err := gitTopLevel(t, inner)
		require.NoError(t, err, "git skips an unreadable .git directory")
		require.Equal(t, realpath(t, outer), realpath(t, want))
		var got string
		captureStderr(t, func() { got, err = LocateRepoRoot() })
		require.NoError(t, err)
		assert.Equal(t, outer, got)
	})
	t.Run("no git: stricter than git", func(t *testing.T) {
		inner, _ := shapeIn(t, gitShapes["mode-000 .git directory"])
		hideGit(t)
		got, err := LocateRepoRoot()
		assert.Empty(t, got)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrNotInRepo)
		assert.Contains(t, err.Error(), filepath.Join(inner, ".git"))
	})
}

// Without git, a commondir that is a FIFO is refused, never opened. (git
// itself blocks reading one, so there is no git answer to compare.)
func TestHandRulesNeverOpenAFIFOCommondir(t *testing.T) {
	inner, _ := shapeIn(t, gitShape{func(t *testing.T, inner string) {
		t.Helper()
		admin := filepath.Join(t.TempDir(), "admin")
		makeGitDirAt(t, admin)
		require.NoError(t, syscall.Mkfifo(filepath.Join(admin, "commondir"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(inner, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))
	}, handError})
	hideGit(t)
	type answer struct {
		dir string
		err error
	}
	done := make(chan answer, 1)
	go func() {
		_, c, err := HandGitDirs(inner)
		done <- answer{c, err}
	}()
	select {
	case a := <-done:
		require.Error(t, a.err)
		assert.Contains(t, a.err.Error(), "not a regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("HandGitDirs blocked on a FIFO commondir")
	}
}

// HandGitDirs names the git dir and the common dir of a linked worktree, and
// says why there is none otherwise.
func TestHandGitDirs(t *testing.T) {
	inner, outer := shapeIn(t, gitShapes["linked worktree"])
	dir, common, err := HandGitDirs(inner)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(outer, ".git", "worktrees", "inner"), dir)
	assert.Equal(t, realpath(t, filepath.Join(outer, ".git")), realpath(t, common))

	_, _, err = HandGitDirs(t.TempDir())
	assert.ErrorIs(t, err, os.ErrNotExist)

	empty, _ := shapeIn(t, gitShapes["empty directory"])
	_, _, err = HandGitDirs(empty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a repository")
}
