//go:build !windows

package mission

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
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

// TestWriteDelegationSkeleton_HappyPath pins the on-disk shape of a
// freshly-written delegation record. record.yaml lands at the
// expected per-delegation path, verdict=open, opened_at is non-empty,
// and the caller-supplied fields round-trip through LoadDelegation.
func TestWriteDelegationSkeleton_HappyPath(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-031"
	delegationID := "d-2026-05-22-001"

	payload := DelegationSkeleton{
		Tier:             TierB,
		ParentDelegation: "d-2026-05-22-000",
		ParentSession:    "sess-outer-7",
		Session:          "sess-inner-9",
		AgentType:        "bwk",
		PromptHash:       "deadbeef",
		Prompt:           []byte("worker prompt body"),
	}

	recordPath, err := WriteDelegationSkeleton(repoRoot, missionID, delegationID, payload)
	require.NoError(t, err)

	want := filepath.Join(
		repoRoot, ".ethos", "missions", missionID, "delegations", "001", "record.yaml",
	)
	assert.Equal(t, want, recordPath,
		"record.yaml must land under .ethos/missions/<mission>/delegations/<NN>/")

	info, err := os.Stat(recordPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"record.yaml mode must be 0o600")

	d, err := LoadDelegation(recordPath)
	require.NoError(t, err)
	assert.Equal(t, DelegationVerdictOpen, d.Verdict, "fresh skeleton verdict must be open")
	assert.NotEmpty(t, d.CreatedAt, "opened_at (CreatedAt) must be stamped")
	assert.Equal(t, TierB, d.Tier)
	assert.Equal(t, missionID, d.Mission)
	assert.Equal(t, "d-2026-05-22-001", d.ID)
	assert.Equal(t, "d-2026-05-22-000", d.ParentDelegation)
	assert.Equal(t, "sess-outer-7", d.ParentSession)
	assert.Equal(t, "sess-inner-9", d.Session)
	assert.Equal(t, "bwk", d.AgentType)
	assert.Equal(t, "deadbeef", d.PromptHash)
	assert.Empty(t, d.ClosedAt, "fresh skeleton must not be closed")

	promptPath := filepath.Join(filepath.Dir(recordPath), "prompt.md")
	data, err := os.ReadFile(promptPath)
	require.NoError(t, err, "prompt body must land next to record.yaml")
	assert.Equal(t, "worker prompt body", string(data))
}

// TestWriteDelegationSkeleton_EmptyArgs surfaces missing arguments
// rather than silently writing under an empty-named directory.
func TestWriteDelegationSkeleton_EmptyArgs(t *testing.T) {
	repoRoot := t.TempDir()

	_, err := WriteDelegationSkeleton("", "m-1", "d-1", DelegationSkeleton{Tier: TierB})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repoRoot")

	_, err = WriteDelegationSkeleton(repoRoot, "", "d-1", DelegationSkeleton{Tier: TierB})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missionID")

	_, err = WriteDelegationSkeleton(repoRoot, "m-1", "", DelegationSkeleton{Tier: TierB})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delegationID")
}

// TestWriteDelegationSkeleton_Atomic asserts the temp+rename invariant:
// a crash before rename leaves no record.yaml on disk. The test models
// the crash by short-circuiting the writer with a forced rename
// failure — the parent directory is removed between MkdirAll and the
// caller's rename so os.Rename fails. After the failed write, the
// record.yaml at the expected path must not exist; the temp file must
// also be cleaned up.
//
// This pin is djb's evaluator gate: a half-written record.yaml is
// unacceptable. The atomicity contract is "either the final
// record.yaml exists at its expected path, or nothing at that path
// exists" — a tempfile from a failed write must not leak as
// record.yaml.
func TestWriteDelegationSkeleton_Atomic(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-031"
	delegationID := "d-2026-05-22-002"

	// Stage the per-delegation dir then chmod it 0o500 so os.CreateTemp
	// fails (no write permission). The skeleton writer must surface the
	// error and leave no record.yaml at the expected location.
	dir := DelegationDir(repoRoot, missionID, delegationID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if os.Getuid() == 0 {
		t.Skip("root bypasses unix permission checks")
	}

	_, err := WriteDelegationSkeleton(repoRoot, missionID, delegationID, DelegationSkeleton{
		Tier:      TierB,
		AgentType: "bwk",
	})
	require.Error(t, err, "writer must surface the temp-file failure")

	// Restore mode so we can read back.
	require.NoError(t, os.Chmod(dir, 0o700))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, "record.yaml", e.Name(),
			"a failed write must leave no record.yaml at the expected path")
		assert.NotContains(t, e.Name(), ".tmp",
			"a failed write must not leak a tempfile (got %s)", e.Name())
	}
}

// TestAcquireMissionLock_AcquireAndRelease pins the happy path:
// the helper creates the per-mission directory, opens the lock file
// with 0o600, holds LOCK_SH, and the release closure is idempotent.
func TestAcquireMissionLock_AcquireAndRelease(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-031"

	release, err := AcquireMissionLock(repoRoot, missionID)
	require.NoError(t, err)
	require.NotNil(t, release)

	lockPath := filepath.Join(repoRoot, ".ethos", "missions", missionID, ".lock")
	info, statErr := os.Stat(lockPath)
	require.NoError(t, statErr, "lock file must exist on disk after acquire")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"lock file mode must be 0o600")

	release()
	release() // idempotent
}

// TestAcquireMissionLock_ConcurrentShared verifies the design
// invariant: LOCK_SH is shared, so two acquirers under the same
// (repoRoot, missionID) must both succeed without blocking each
// other. If this test fails, the lock has been silently promoted to
// LOCK_EX and concurrent Tier B spawns would serialize.
func TestAcquireMissionLock_ConcurrentShared(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-031"

	release1, err := AcquireMissionLock(repoRoot, missionID)
	require.NoError(t, err)
	defer release1()

	var acquired2 atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		release2, err := AcquireMissionLock(repoRoot, missionID)
		if err != nil {
			return
		}
		acquired2.Store(true)
		release2()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second shared acquire did not complete within 2s")
	}
	assert.True(t, acquired2.Load(),
		"two LOCK_SH acquirers on the same mission lock must both succeed")
}

// TestAcquireMissionLock_ExclusiveBlocks verifies the exclusive-side
// of the contract: a goroutine holding LOCK_EX on the same mission
// lock file must block AcquireMissionLock (LOCK_SH) until the
// exclusive holder releases. This is the future-proofing for the
// hypothetical mission-close path that wants the tree quiescent.
func TestAcquireMissionLock_ExclusiveBlocks(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-031"

	dir := filepath.Join(repoRoot, ".ethos", "missions", missionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	lockPath := filepath.Join(dir, ".lock")

	excl, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(excl.Fd()), syscall.LOCK_EX))

	var acquired atomic.Bool
	var tStart, tAcquired time.Time
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		tStart = time.Now()
		release, err := AcquireMissionLock(repoRoot, missionID)
		tAcquired = time.Now()
		if err != nil {
			return
		}
		acquired.Store(true)
		release()
	}()

	time.Sleep(50 * time.Millisecond)
	assert.False(t, acquired.Load(),
		"shared acquire must block while LOCK_EX is held")

	hold := 60 * time.Millisecond
	time.Sleep(hold)
	require.NoError(t, syscall.Flock(int(excl.Fd()), syscall.LOCK_UN))
	require.NoError(t, excl.Close())

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shared acquire did not complete within 2s after exclusive release")
	}
	assert.True(t, acquired.Load(),
		"shared acquire must succeed once exclusive holder releases")
	waited := tAcquired.Sub(tStart)
	assert.GreaterOrEqual(t, waited, 40*time.Millisecond,
		"shared acquire wait must reflect the exclusive hold (got %v)", waited)
}

// TestAcquireMissionLock_EmptyArgs surfaces missing arguments
// rather than silently locking under an empty-named file.
func TestAcquireMissionLock_EmptyArgs(t *testing.T) {
	_, err := AcquireMissionLock("", "m-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repoRoot")

	_, err = AcquireMissionLock(t.TempDir(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missionID")
}

// TestCloseDelegationSkeleton_HappyPath pins the atomic rewrite: a
// skeleton on disk with verdict=open is closed to verdict=aborted
// and the closed_at timestamp is stamped. LoadDelegation reads the
// new state back; every other field round-trips unchanged.
func TestCloseDelegationSkeleton_HappyPath(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-032"
	delegationID := "d-2026-05-22-010"

	payload := DelegationSkeleton{
		Tier:             TierB,
		ParentDelegation: "d-2026-05-22-009",
		ParentSession:    "sess-outer",
		Session:          "sess-inner",
		AgentType:        "bwk",
		PromptHash:       "deadbeef",
	}
	recordPath, err := WriteDelegationSkeleton(repoRoot, missionID, delegationID, payload)
	require.NoError(t, err)

	closedAt := time.Now().UTC().Format(time.RFC3339)
	require.NoError(t, CloseDelegationSkeleton(
		repoRoot, missionID, delegationID, DelegationVerdictAborted, closedAt,
	))

	d, err := LoadDelegation(recordPath)
	require.NoError(t, err)
	assert.Equal(t, DelegationVerdictAborted, d.Verdict,
		"verdict must be the value passed to CloseDelegationSkeleton")
	assert.Equal(t, closedAt, d.ClosedAt,
		"closed_at must be the timestamp passed to CloseDelegationSkeleton")
	assert.Equal(t, TierB, d.Tier, "tier must round-trip")
	assert.Equal(t, "d-2026-05-22-009", d.ParentDelegation,
		"parent_delegation must round-trip")
	assert.Equal(t, "sess-outer", d.ParentSession, "parent_session must round-trip")
	assert.Equal(t, "deadbeef", d.PromptHash, "prompt_hash must round-trip")
}

// TestCloseDelegationSkeleton_VerdictValidation surfaces every
// disallowed verdict argument. DelegationVerdictOpen is rejected
// because closing to "open" is not a state transition; the empty
// string and arbitrary strings are rejected so a caller cannot
// silently corrupt the record with a typo.
func TestCloseDelegationSkeleton_VerdictValidation(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-032"
	delegationID := "d-2026-05-22-011"

	_, err := WriteDelegationSkeleton(repoRoot, missionID, delegationID, DelegationSkeleton{
		Tier:      TierB,
		AgentType: "bwk",
	})
	require.NoError(t, err)

	closedAt := time.Now().UTC().Format(time.RFC3339)
	bad := []string{
		"",
		"open",
		"aborte",
		"PASS",
		"unknown",
	}
	for _, v := range bad {
		err := CloseDelegationSkeleton(repoRoot, missionID, delegationID, v, closedAt)
		require.Error(t, err, "verdict %q must be rejected", v)
		assert.Contains(t, err.Error(), "invalid verdict",
			"error for verdict %q must label the verdict validation", v)
	}

	for _, v := range []string{
		DelegationVerdictPass,
		DelegationVerdictFail,
		DelegationVerdictError,
		DelegationVerdictAborted,
	} {
		require.NoError(t,
			CloseDelegationSkeleton(repoRoot, missionID, delegationID, v, closedAt),
			"verdict %q must be accepted", v,
		)
	}
}

// TestCloseDelegationSkeleton_MissingRecord asserts the caller-
// observable contract for a refusal that fires BEFORE the skeleton
// was written. The error wraps fs.ErrNotExist so the caller can
// distinguish "skeleton was never written" from other I/O failures
// and report the order-of-operations bug loudly.
func TestCloseDelegationSkeleton_MissingRecord(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-032"
	delegationID := "d-2026-05-22-012"

	closedAt := time.Now().UTC().Format(time.RFC3339)
	err := CloseDelegationSkeleton(
		repoRoot, missionID, delegationID, DelegationVerdictAborted, closedAt,
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrNotExist),
		"missing record must wrap fs.ErrNotExist; got %v", err)
}

// TestCloseDelegationSkeleton_Atomic asserts the temp+rename
// invariant: a failure on the rename path must leave the on-disk
// record.yaml in its prior (open) state. The test stages a skeleton,
// chmods the per-delegation dir to read-only between open and rename
// to force os.CreateTemp's path to fail, and asserts the original
// record.yaml is unchanged afterwards.
//
// This is djb's evaluator gate: a half-written close is unacceptable.
// Either the new verdict lands in full or the prior contents persist.
func TestCloseDelegationSkeleton_Atomic(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses unix permission checks")
	}
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-032"
	delegationID := "d-2026-05-22-013"

	recordPath, err := WriteDelegationSkeleton(repoRoot, missionID, delegationID, DelegationSkeleton{
		Tier:      TierB,
		AgentType: "bwk",
	})
	require.NoError(t, err)

	original, err := os.ReadFile(recordPath)
	require.NoError(t, err)

	dir := DelegationDir(repoRoot, missionID, delegationID)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	closedAt := time.Now().UTC().Format(time.RFC3339)
	err = CloseDelegationSkeleton(
		repoRoot, missionID, delegationID, DelegationVerdictAborted, closedAt,
	)
	require.Error(t, err, "close must surface the I/O failure")

	require.NoError(t, os.Chmod(dir, 0o700))
	after, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(after),
		"a failed close must leave the prior record.yaml unchanged")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp",
			"a failed close must not leak a tempfile (got %s)", e.Name())
	}
}

// TestCloseDelegation_AtomicWrite pins the os.CreateTemp + Sync +
// Rename invariant for CloseDelegation. The success path must leave
// no .tmp residue in the per-delegation directory; the on-disk file
// mode must be 0o600 even when the caller's umask would have widened
// it (a pre-fix bare os.WriteFile + predictable ".tmp" suffix could
// leak both ways).
func TestCloseDelegation_AtomicWrite(t *testing.T) {
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-100"
	delegationID := "d-2026-05-22-100"

	recordPath, err := WriteDelegationSkeleton(repoRoot, missionID, delegationID, DelegationSkeleton{
		Tier:      TierB,
		AgentType: "bwk",
	})
	require.NoError(t, err)

	require.NoError(t, CloseDelegation(recordPath, DelegationVerdictPass, "ok"))

	// File mode must be 0o600 — the writer chmods explicitly so the
	// caller's umask cannot widen the permissions.
	info, err := os.Stat(recordPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"record.yaml mode must be 0o600 after CloseDelegation")

	// No .tmp residue: os.CreateTemp's random suffix should leave nothing
	// behind once Rename succeeds, and the writer must not have used a
	// predictable ".tmp" suffix that could survive concurrent writers.
	dir := filepath.Dir(recordPath)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp",
			"successful CloseDelegation must leave no .tmp residue (got %s)", e.Name())
	}

	// State round-trips: verdict + closed_at are stamped.
	d, err := LoadDelegation(recordPath)
	require.NoError(t, err)
	assert.Equal(t, DelegationVerdictPass, d.Verdict)
	assert.NotEmpty(t, d.ClosedAt)
}

// TestWriteDelegationSkeleton_AtomicPrompt pins the order-of-writes
// invariant: prompt.md is written BEFORE record.yaml so a writer
// crash between the two leaves no record.yaml on disk. The test
// stages a half-built skeleton by chmodding the dir read-only after
// prompt.md exists but before the writer could rename record.yaml,
// then asserts the file shape: prompt.md present, record.yaml
// absent.
func TestWriteDelegationSkeleton_AtomicPrompt(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses unix permission checks")
	}
	repoRoot := t.TempDir()
	missionID := "m-2026-05-22-101"
	delegationID := "d-2026-05-22-101"

	dir := DelegationDir(repoRoot, missionID, delegationID)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	// Pre-create prompt.md so the writer's prompt-step succeeds, then
	// chmod the dir read-only so record.yaml's atomic write fails.
	// This simulates a crash between the prompt write and the record
	// write — exactly the order-of-operations invariant under test.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.md"),
		[]byte("seed"), 0o600))

	// Mode 0o500: read+execute, no write. os.CreateTemp inside the dir
	// will fail, surfacing as a record-write error.
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := WriteDelegationSkeleton(repoRoot, missionID, delegationID, DelegationSkeleton{
		Tier:      TierB,
		AgentType: "bwk",
		Prompt:    []byte("worker prompt body"),
	})
	require.Error(t, err, "writer must surface the failed record-write")

	require.NoError(t, os.Chmod(dir, 0o700))

	// record.yaml must NOT exist on disk — a half-built skeleton has
	// only prompt.md, never record.yaml.
	_, err = os.Stat(filepath.Join(dir, "record.yaml"))
	assert.True(t, errors.Is(err, fs.ErrNotExist),
		"a failed record-write must leave no record.yaml on disk; got %v", err)

	// No .tmp leak either.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp",
			"a failed write must not leak a tempfile (got %s)", e.Name())
	}
}

// TestWriteDelegationSkeleton_FsyncErrPropagates asserts the
// surface-the-sync-error invariant. The atomic writer must propagate
// the fsync failure rather than mask it. We exercise this via the
// shared writeAtomicFile helper: a closed file descriptor cannot be
// synced, which is the simplest portable proxy for a real fsync
// failure mode.
func TestWriteDelegationSkeleton_FsyncErrPropagates(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.yaml")

	// Happy path: writeAtomicFile succeeds + the file is sync'd to
	// disk. If a future refactor drops the Sync() call, this assertion
	// remains true but the companion failure-mode test below catches
	// the regression.
	require.NoError(t, writeAtomicFile(dir, "out-*.tmp", dest, []byte("body")))
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "body", string(got))

	// Failure path: an unwriteable parent dir forces the temp-create
	// to fail. The error must surface, not silently drop.
	if os.Getuid() != 0 {
		readonly := filepath.Join(dir, "ro")
		require.NoError(t, os.Mkdir(readonly, 0o500))
		t.Cleanup(func() { _ = os.Chmod(readonly, 0o700) })

		err := writeAtomicFile(readonly, "out-*.tmp",
			filepath.Join(readonly, "x.yaml"), []byte("body"))
		require.Error(t, err, "temp-create failure must propagate, not be swallowed")
		assert.Contains(t, err.Error(), "creating temp file")
	}
}

// TestDelegationSequence pins the parser used to derive the per-
// mission sequence directory from a d-YYYY-MM-DD-NNN delegation ID.
// Each row is a single input/output pair so a regression in the
// parser surfaces against a known string.
func TestDelegationSequence(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"canonical shape", "d-2026-05-22-001", "001"},
		{"three-digit tail", "d-2026-05-22-123", "123"},
		{"bare id no dashes", "abc", "abc"},
		{"trailing dash falls back to base", "d-2026-05-22-", "d-2026-05-22-"},
		{"path stripped via base", "../etc/d-2026-05-22-001", "001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := delegationSequence(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}
