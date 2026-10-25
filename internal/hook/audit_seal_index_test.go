package hook

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// treePaths lists every path under root, directories included, except the
// per-session flock files the live zone keeps by design (they are machine-local
// and gitignored, and a seal that fails leaves them as one that succeeds does).
func treePaths(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, ".lock") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel)
		return nil
	}))
	return out
}

// A seal that fails leaves no trace: the directories it made are removed with
// its chunks, and the snapshot of the index is gone. The same through a
// symlinked repository root.
func TestFailedSealLeavesNoDirectoriesOrSnapshot(t *testing.T) {
	for _, viaLink := range []bool{false, true} {
		name := map[bool]string{false: "direct", true: "symlinked root"}[viaLink]
		t.Run(name, func(t *testing.T) {
			real := t.TempDir()
			initGitRepo(t, real)
			require.NoError(t, os.WriteFile(filepath.Join(real, ".gitignore"), []byte(".punt-labs/ethos/sessions/*sess-two\n"), 0o644))
			writeLiveLines(t, real, "sess-one", 100, 200)
			writeLiveLines(t, real, "sess-two", 300, 400)
			repo := real
			if viaLink {
				repo = filepath.Join(t.TempDir(), "link")
				require.NoError(t, os.Symlink(real, repo))
			}
			before := treePaths(t, real)

			_, err := SealRepo(repo, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC), SealOptions{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "the tree is unchanged")
			assert.Equal(t, before, treePaths(t, real), "no chunk, no directory this run made survives")
			leftovers, gerr := filepath.Glob(filepath.Join(real, ".git", "*.ethos-tmp"))
			require.NoError(t, gerr)
			assert.Empty(t, leftovers)
		})
	}
}

// git add that cannot take index.lock changed nothing: the lock is left alone,
// nothing is restored, the error says so, and no snapshot or directory is left.
func TestSealLeavesAnExistingIndexLockAlone(t *testing.T) {
	for _, viaLink := range []bool{false, true} {
		name := map[bool]string{false: "direct", true: "symlinked root"}[viaLink]
		t.Run(name, func(t *testing.T) {
			real := t.TempDir()
			initGitRepo(t, real)
			writeLiveLines(t, real, "sess-one", 100, 200)
			repo := real
			if viaLink {
				repo = filepath.Join(t.TempDir(), "link")
				require.NoError(t, os.Symlink(real, repo))
			}
			lock := filepath.Join(real, ".git", "index.lock")
			require.NoError(t, os.WriteFile(lock, []byte("theirs"), 0o644))
			indexBefore, err := os.ReadFile(filepath.Join(real, ".git", "index"))
			require.NoError(t, err)
			before := treePaths(t, real)

			_, err = SealRepo(repo, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC), SealOptions{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "index.lock, which already exists")
			assert.Contains(t, err.Error(), "left alone")
			got, rerr := os.ReadFile(lock)
			require.NoError(t, rerr, "the lock is still there")
			assert.Equal(t, "theirs", string(got))
			indexAfter, rerr := os.ReadFile(filepath.Join(real, ".git", "index"))
			require.NoError(t, rerr)
			assert.Equal(t, indexBefore, indexAfter)
			assert.Equal(t, before, treePaths(t, real))
			leftovers, gerr := filepath.Glob(filepath.Join(real, ".git", "*.ethos-tmp"))
			require.NoError(t, gerr)
			assert.Empty(t, leftovers)
		})
	}
}

// What a rollback could not remove is named, empty directories included, and
// the message does not then claim the tree is unchanged.
func TestRollbackNamesAnEmptyDirectoryItCouldNotRemove(t *testing.T) {
	dir := t.TempDir()
	made := filepath.Join(dir, "made")
	require.NoError(t, os.Mkdir(made, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(made, "someone-elses"), []byte("x"), 0o644)) // not empty: Remove fails
	err := rollbackSeal(nil, nil, []string{made}, assert.AnError, "writing chunks")
	assert.Contains(t, err.Error(), made)
	assert.NotContains(t, err.Error(), "the tree is unchanged")
}

// appendMissingDirs lists only what creating the directory would make.
func TestAppendMissingDirs(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
	got := appendMissingDirs(nil, root, filepath.Join(root, "a", "b", "c"))
	assert.Equal(t, []string{filepath.Join(root, "a", "b"), filepath.Join(root, "a", "b", "c")}, got)
	assert.Equal(t, got, appendMissingDirs(got, root, filepath.Join(root, "a", "b", "c")), "no repeats")
	assert.Empty(t, appendMissingDirs(nil, root, filepath.Join(root, "a")))
}
