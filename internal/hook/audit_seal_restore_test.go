package hook

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexFixture writes an index holding before and snapshots it, then writes
// afterAdd as the index our `git add` left and marks the add finished.
func indexFixture(t *testing.T, mode os.FileMode, before, afterAdd string) (*indexSnapshot, string, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index")
	require.NoError(t, os.WriteFile(path, []byte(before), mode))
	require.NoError(t, os.Chmod(path, mode))
	snap, err := snapshotIndex(path)
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, os.WriteFile(path, []byte(afterAdd), mode))
	snap.addFinished(start)
	return snap, path, start
}

// The snapshot keeps the index's mode, and so does the restored index.
func TestIndexSnapshotKeepsTheMode(t *testing.T) {
	snap, path, _ := indexFixture(t, 0o640, "before", "after")
	info, err := os.Stat(snap.snap)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "the snapshot has the index's mode")

	require.NoError(t, snap.restore())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "before", string(data))
	info, err = os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "the restored index has the index's mode")
	assert.NoFileExists(t, path+".lock", "the lock is released")
}

// The restore takes <index>.lock as git does, so a git that is writing the
// index is never raced; a lock that is not ours to remove is named.
func TestIndexRestoreRespectsTheLock(t *testing.T) {
	snap, path, _ := indexFixture(t, 0o644, "before", "after")
	require.NoError(t, os.WriteFile(path+".lock", nil, 0o644))
	require.NoError(t, os.Chtimes(path+".lock", time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)))

	err := snap.restore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), path+".lock")
	data, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, "after", string(data), "the index is left alone")
	assert.FileExists(t, path+".lock", "a lock older than our add is not removed")
}

// A lock our killed git left behind (newer than our add, held by no process)
// is removed, and the index restored.
func TestIndexRestoreRemovesOurStaleLock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("checking that no process holds the lock needs /proc")
	}
	snap, path, _ := indexFixture(t, 0o644, "before", "after")
	require.NoError(t, os.WriteFile(path+".lock", []byte("partial"), 0o644))

	require.NoError(t, snap.restore())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "before", string(data))
	assert.NoFileExists(t, path+".lock")
}

// A lock a live process holds is never removed, however new.
func TestIndexRestoreKeepsAHeldLock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("checking that no process holds the lock needs /proc")
	}
	snap, path, _ := indexFixture(t, 0o644, "before", "after")
	f, err := os.Create(path + ".lock")
	require.NoError(t, err)
	defer f.Close()

	err = snap.restore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), path+".lock")
	assert.FileExists(t, path+".lock")
	data, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, "after", string(data))
}

// The restore puts the snapshot back only if the index is still what our add
// left. Another writer's index is left, and the error says so.
func TestIndexRestoreLeavesAnotherWritersIndex(t *testing.T) {
	snap, path, _ := indexFixture(t, 0o644, "before", "after")
	require.NoError(t, os.WriteFile(path, []byte("someone else"), 0o644))

	err := snap.restore()
	require.ErrorIs(t, err, errIndexChanged)
	data, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, "someone else", string(data))
	assert.NoFileExists(t, path+".lock")
}

// With no index before the add (a repository with no commits), the restore
// removes the one git made, under the lock.
func TestIndexRestoreRemovesTheIndexGitMade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	snap, err := snapshotIndex(path)
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, os.WriteFile(path, []byte("made by add"), 0o644))
	snap.addFinished(start)
	require.NoError(t, snap.restore())
	assert.NoFileExists(t, path)
	assert.NoFileExists(t, path+".lock")
}

// A failure names its phase whether or not the rollback finished.
func TestSealStageFailureAlwaysNamesThePhase(t *testing.T) {
	for _, left := range [][]string{nil, {"restoring the index: boom"}} {
		err := sealStageFailure("/r", "staging chunks (git add)", assert.AnError, left)
		assert.Contains(t, err.Error(), "staging chunks (git add)")
	}
}
