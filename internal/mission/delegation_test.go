//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAcquireDelegationLock_AcquireAndRelease pins the happy path:
// the helper creates the parent directory, opens the lock file with
// 0o600, holds LOCK_EX, and the returned release closure runs cleanly.
// Calling release twice is a no-op.
func TestAcquireDelegationLock_AcquireAndRelease(t *testing.T) {
	repoRoot := t.TempDir()
	delegationID := "d-2026-05-22-001"

	release, err := AcquireDelegationLock(repoRoot, delegationID)
	require.NoError(t, err)
	require.NotNil(t, release)

	lockPath := filepath.Join(repoRoot, ".ethos", "delegations", delegationID+".lock")
	info, statErr := os.Stat(lockPath)
	require.NoError(t, statErr, "lock file must exist on disk after acquire")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"lock file mode must be 0o600")

	// Release once: clean.
	release()
	// Release twice: idempotent no-op (must not panic, must not error).
	release()
}

// TestAcquireDelegationLock_BlocksUntilRelease verifies the sibling-
// goroutine block contract. A second AcquireDelegationLock against
// the same (repoRoot, delegationID) must block until the first
// release fires, then return cleanly. The test uses a 50ms hold on
// the first acquire and asserts the second acquire's wait time is
// at least 40ms — slack for scheduler jitter without false-negative
// risk on a loaded CI host.
func TestAcquireDelegationLock_BlocksUntilRelease(t *testing.T) {
	repoRoot := t.TempDir()
	delegationID := "d-2026-05-22-002"

	release1, err := AcquireDelegationLock(repoRoot, delegationID)
	require.NoError(t, err)

	var acquired2 atomic.Bool
	var t2Start time.Time
	var t2Acquired time.Time

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		t2Start = time.Now()
		release2, err := AcquireDelegationLock(repoRoot, delegationID)
		t2Acquired = time.Now()
		require.NoError(t, err)
		acquired2.Store(true)
		release2()
	}()

	// Give the goroutine time to enter Flock and block. A short sleep
	// is acceptable here: the assertion is on observable order, not on
	// the exact duration.
	time.Sleep(50 * time.Millisecond)
	assert.False(t, acquired2.Load(),
		"sibling acquire must block while first holder is live")

	hold := 50 * time.Millisecond
	time.Sleep(hold)
	release1()

	// Wait for the sibling goroutine to acquire and release.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sibling goroutine did not acquire within 2s after release")
	}

	assert.True(t, acquired2.Load(), "sibling must acquire after first release")
	waited := t2Acquired.Sub(t2Start)
	assert.GreaterOrEqual(t, waited, 40*time.Millisecond,
		"sibling acquire wait must reflect the hold (got %v)", waited)
}

// TestAcquireDelegationLock_EmptyArgs surfaces missing arguments
// rather than silently locking under an empty-named file.
func TestAcquireDelegationLock_EmptyArgs(t *testing.T) {
	_, err := AcquireDelegationLock("", "d-001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repoRoot")

	_, err = AcquireDelegationLock(t.TempDir(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delegationID")
}

// TestAcquireDelegationLock_BaseName confirms that a delegation ID
// containing path separators is sanitized via filepath.Base before
// being used as a filename — defense against an upstream caller
// that hands the helper a tainted ID.
func TestAcquireDelegationLock_BaseName(t *testing.T) {
	repoRoot := t.TempDir()
	release, err := AcquireDelegationLock(repoRoot, "../../etc/passwd")
	require.NoError(t, err)
	defer release()

	// The lock file must land under .ethos/delegations/, not outside.
	expectedDir := filepath.Join(repoRoot, ".ethos", "delegations")
	entries, err := os.ReadDir(expectedDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "passwd.lock", entries[0].Name(),
		"path separators in delegationID must be stripped via filepath.Base")
}
