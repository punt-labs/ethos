package hook

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexFixture writes an index holding before and snapshots it, then writes
// afterAdd as the index our `git add` left and marks the add finished.
func indexFixture(t *testing.T, mode os.FileMode, before, afterAdd string) (*indexSnapshot, string, time.Time) {
	return indexFixtureTimedOut(t, mode, before, afterAdd, false)
}

// indexFixtureTimedOut is indexFixture for an add that did, or did not, time out.
func indexFixtureTimedOut(t *testing.T, mode os.FileMode, before, afterAdd string, timedOut bool) (*indexSnapshot, string, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index")
	require.NoError(t, os.WriteFile(path, []byte(before), mode))
	require.NoError(t, os.Chmod(path, mode))
	snap, err := snapshotIndex(path)
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, os.WriteFile(path, []byte(afterAdd), mode))
	snap.addFinished(start, timedOut)
	return snap, path, start
}

// The snapshot keeps the index's mode, and so does the restored index.
func TestIndexSnapshotKeepsTheMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not POSIX permissions here")
	}
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
	stubLockHeld(t, false, "")
	snap, path, _ := indexFixtureTimedOut(t, 0o644, "before", "after", true)
	require.NoError(t, os.WriteFile(path+".lock", []byte("partial"), 0o644))

	require.NoError(t, snap.restore())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "before", string(data))
	assert.NoFileExists(t, path+".lock")
	require.Len(t, snap.notes, 1, "a lock that was removed is reported")
	assert.Contains(t, snap.notes[0], path+".lock")
}

// After any failure other than our own add timing out, a lock belongs to
// someone else, however new and however unheld.
func TestIndexRestoreLeavesALockAfterANonTimeoutFailure(t *testing.T) {
	snap, path, _ := indexFixtureTimedOut(t, 0o644, "before", "after", false)
	require.NoError(t, os.WriteFile(path+".lock", []byte("theirs"), 0o644))
	err := snap.restore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), path+".lock")
	assert.FileExists(t, path+".lock")
	assert.Empty(t, snap.notes)
}

// The holder check compares inodes: a lock reached through a symlinked
// directory is still found open.
func TestLockHeldComparesByInode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc")
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))
	f, err := os.Create(filepath.Join(real, "index.lock"))
	require.NoError(t, err)
	held, _ := lockHeldOnThisSystem(filepath.Join(link, "index.lock"))
	assert.True(t, held, "held through another spelling of the path")
	require.NoError(t, f.Close())
	held, _ = lockHeldOnThisSystem(filepath.Join(link, "index.lock"))
	assert.False(t, held)
}

// A lock a live process holds is never removed, however new.
func TestIndexRestoreKeepsAHeldLock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("checking that no process holds the lock needs /proc")
	}
	snap, path, _ := indexFixtureTimedOut(t, 0o644, "before", "after", true)
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
	snap.addFinished(start, false)
	require.NoError(t, snap.restore())
	assert.NoFileExists(t, path)
	assert.NoFileExists(t, path+".lock")
}

// A failure names its phase whether or not the rollback finished.
func TestSealStageFailureAlwaysNamesThePhase(t *testing.T) {
	for _, left := range [][]string{nil, {"restoring the index: boom"}} {
		err := sealStageFailure("staging chunks (git add)", assert.AnError, left)
		assert.Contains(t, err.Error(), "staging chunks (git add)")
	}
}

// stubLockHeld replaces the holder check for one test.
func stubLockHeld(t *testing.T, held bool, unknown string) {
	t.Helper()
	orig := lockHeld
	t.Cleanup(func() { lockHeld = orig })
	lockHeld = func(string) (bool, string) { return held, unknown }
}

// When the holder check could not look at every process (another user's), a
// lock is never removed, and the reason is in the message.
func TestIndexRestoreKeepsALockItCannotVouchFor(t *testing.T) {
	stubLockHeld(t, false, "could not check the processes of other users")
	snap, path, _ := indexFixtureTimedOut(t, 0o644, "before", "after", true)
	require.NoError(t, os.WriteFile(path+".lock", []byte("maybe theirs"), 0o644))
	err := snap.restore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not check the processes of other users")
	assert.FileExists(t, path+".lock")
	assert.Empty(t, snap.notes)
}

// The real check says so when some process's fds are unreadable to it.
func TestLockHeldReportsUnreadableProcesses(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("needs /proc and a non-root user")
	}
	procs, err := os.ReadDir("/proc")
	require.NoError(t, err)
	foreign := false
	for _, p := range procs {
		if _, aerr := strconv.Atoi(p.Name()); aerr != nil {
			continue
		}
		if _, rerr := os.ReadDir(filepath.Join("/proc", p.Name(), "fd")); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			foreign = true
			break
		}
	}
	if !foreign {
		t.Skip("no process of another user here")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "x.lock"))
	require.NoError(t, err)
	defer f.Close()
	other := filepath.Join(t.TempDir(), "free.lock")
	require.NoError(t, os.WriteFile(other, nil, 0o644))
	held, unknown := lockHeldOnThisSystem(other)
	assert.False(t, held)
	assert.Contains(t, unknown, "other users")
}
