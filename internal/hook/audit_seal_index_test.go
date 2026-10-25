package hook

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A `git add` that stages some paths and then fails leaves the index changed.
// The seal restores the index it snapshotted before the add, and removes the
// chunks it wrote, so the tree and the index are as they were; and its error
// says so, naming the phase.
func TestSealRestoresTheIndexWhenAddFailsAfterStagingSome(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	require.NoError(t, err)
	repo := t.TempDir()
	initGitRepo(t, repo)
	// The second session's sealed directory is ignored, so `git add` stages the
	// first session's chunk, then refuses the ignored path and exits non-zero.
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".punt-labs/ethos/sessions/*sess-two\n"), 0o644))
	writeLiveLines(t, repo, "sess-one", 100, 200)
	writeLiveLines(t, repo, "sess-two", 300, 400)
	index := filepath.Join(repo, ".git", "index")
	indexBefore, err := os.ReadFile(index)
	require.NoError(t, err)
	statusBefore := withoutLocks(gitStatus(t, gitBin, repo))
	files := sealedFiles(t, repo)

	_, err = SealRepo(repo, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC), SealOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "staging chunks (git add)")
	assert.Contains(t, err.Error(), "the commit is blocked and the tree is unchanged")

	indexAfter, rerr := os.ReadFile(index)
	require.NoError(t, rerr)
	assert.Equal(t, indexBefore, indexAfter, "the index is byte-for-byte what it was")
	assert.Equal(t, statusBefore, withoutLocks(gitStatus(t, gitBin, repo)), "git status is what it was")
	assert.Equal(t, files, sealedFiles(t, repo), "no chunk this run wrote survives")
	leftovers, gerr := filepath.Glob(filepath.Join(repo, ".git", "*.ethos-tmp"))
	require.NoError(t, gerr)
	assert.Empty(t, leftovers, "the snapshot is removed")
}

// The error claims the tree is unchanged only when that is true: if the
// restore itself fails, it says what is left.
func TestSealErrorDoesNotClaimUnchangedWhenRollbackFails(t *testing.T) {
	cause := assert.AnError
	err := sealStageFailure("staging chunks (git add)", cause, []string{"restoring the index: boom"})
	assert.NotContains(t, err.Error(), "the tree is unchanged")
	assert.Contains(t, err.Error(), "restoring the index: boom")
	ok := sealStageFailure("staging chunks (git add)", cause, nil)
	assert.Contains(t, ok.Error(), "the commit is blocked and the tree is unchanged")
}

// A nil error is never classified as a timeout: SealGitError names a timeout
// only for an error that is one.
func TestSealGitErrorClassifiesOnlyARealTimeout(t *testing.T) {
	plain := SealGitError("/r", "reading the status", errors.New("fatal: boom"), true)
	assert.NotContains(t, plain.Error(), "did not answer")
	assert.Contains(t, plain.Error(), "fatal: boom")
	to := SealGitError("/r", "reading the status", fmt.Errorf("x: %w", gitenv.ErrTimeout), true)
	assert.Contains(t, to.Error(), "did not answer in /r while reading the status")
	nilErr := SealGitError("/r", "reading the status", nil, true)
	assert.NotContains(t, nilErr.Error(), "did not answer")
}
