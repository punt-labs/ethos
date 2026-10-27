//go:build linux || darwin

package resolve

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	// git skips a .git that is neither a directory nor a regular file, and
	// opening a FIFO would block the walk forever.
	gitShapes["fifo"] = gitShape{func(t *testing.T, inner string) {
		t.Helper()
		require.NoError(t, syscall.Mkfifo(filepath.Join(inner, ".git"), 0o644))
	}, handError}

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
		// ethos does not: answering for the enclosing repository would be a wrong
		// answer for the one the user is in.
		var got string
		captureStderr(t, func() { got, err = locateRepoRoot() })
		assert.Empty(t, got)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrNotInRepo)
		assert.Contains(t, err.Error(), filepath.Join(inner, ".git"))
	})
	t.Run("no git: stricter than git", func(t *testing.T) {
		inner, _ := shapeIn(t, gitShapes["mode-000 .git directory"])
		hideGit(t)
		got, err := locateRepoRoot()
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

// within runs fn and fails the test if it has not returned by limit.
func within(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("still running after %v", limit)
	}
}

// git itself blocks reading a FIFO commondir. Its command is stopped after
// the gitenv deadline (shortened here) and the answer is a refusal naming git and the directory:
// never a hang, never the hand rules in its place. IsWorkingTree reports the
// same refusal rather than "not a working tree".
func TestGitThatHangsIsARefusal(t *testing.T) {
	requireGit(t)
	testhelpers.GitTimeout(t, 200*time.Millisecond)
	inner, _ := shapeIn(t, gitShape{func(t *testing.T, inner string) {
		t.Helper()
		admin := filepath.Join(t.TempDir(), "admin")
		makeGitDirAt(t, admin)
		require.NoError(t, syscall.Mkfifo(filepath.Join(admin, "commondir"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(inner, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))
	}, handError})
	var root string
	var err, wtErr error
	within(t, 10*time.Second, func() {
		captureStderr(t, func() { root, err = locateRepoRoot() })
		_, wtErr = IsWorkingTree(inner)
	})
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.NotErrorIs(t, err, ErrNotInRepo)
	assert.Empty(t, root)
	assert.Contains(t, err.Error(), "git")
	assert.Contains(t, err.Error(), inner)
	assert.ErrorIs(t, wtErr, gitenv.ErrTimeout)
}

// A git whose --version does not answer is a refusal too, not a silent
// switch to the hand rules.
func TestGitProbeThatHangsIsARefusal(t *testing.T) {
	testhelpers.GitTimeout(t, 200*time.Millisecond)
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	fakeGit(t, "exec "+sleep+" 60\n")
	dir := t.TempDir()
	testhelpers.MakeGitDir(t, dir)
	t.Chdir(dir)
	within(t, 10*time.Second, func() {
		captureStderr(t, func() { _, err = locateRepoRoot() })
	})
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.Contains(t, err.Error(), "--version")
}
