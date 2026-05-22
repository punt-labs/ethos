//go:build !windows

package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewID_FirstCounter(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	id, release, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)
	defer release(true)
	assert.Equal(t, "m-2026-04-07-001", id)
}

func TestNewID_DelegationNamespace(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	id, release, err := NewIDAt(root, NamespaceDelegations, now)
	require.NoError(t, err)
	defer release(true)
	assert.Equal(t, "d-2026-04-07-001", id)
}

func TestNewID_GenericNamespace(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	id, release, err := NewIDAt(root, "audits", now)
	require.NoError(t, err)
	defer release(true)
	assert.Equal(t, "audits-2026-04-07-001", id)
}

func TestNewID_SiblingCountersAreIndependent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	// Two missions, two delegations. The mission counter and the
	// delegation counter advance independently — DES-054 I9-counter
	// says new namespaces add sibling files and never affect existing
	// ones.
	m1, rel, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)
	rel(true)
	assert.Equal(t, "m-2026-04-07-001", m1)

	d1, rel, err := NewIDAt(root, NamespaceDelegations, now)
	require.NoError(t, err)
	rel(true)
	assert.Equal(t, "d-2026-04-07-001", d1)

	m2, rel, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)
	rel(true)
	assert.Equal(t, "m-2026-04-07-002", m2)

	d2, rel, err := NewIDAt(root, NamespaceDelegations, now)
	require.NoError(t, err)
	rel(true)
	assert.Equal(t, "d-2026-04-07-002", d2)
}

func TestNewID_Monotonic(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	const n = 100
	seen := make(map[string]bool, n)
	for i := 1; i <= n; i++ {
		id, release, err := NewIDAt(root, NamespaceMissions, now)
		require.NoError(t, err)
		release(true)
		expected := fmt.Sprintf("m-2026-04-07-%03d", i)
		assert.Equal(t, expected, id)
		assert.False(t, seen[id], "duplicate ID %q", id)
		seen[id] = true
	}
	assert.Len(t, seen, n)
}

// TestNewID_Concurrent runs 10 goroutines each allocating 10 IDs and
// asserts every result is distinct. DES-054 phase 1 success criterion
// for the per-namespace counter atomicity under 10-goroutine
// concurrent NewID calls.
func TestNewID_Concurrent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	const goroutines = 10
	const perGoroutine = 10
	const total = goroutines * perGoroutine

	results := make(chan string, total)
	errs := make(chan error, total)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id, release, err := NewIDAt(root, NamespaceMissions, now)
				if err != nil {
					errs <- err
					return
				}
				release(true)
				results <- id
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	seen := make(map[string]bool, total)
	for id := range results {
		assert.False(t, seen[id], "duplicate ID %q observed under concurrent NewID", id)
		seen[id] = true
	}
	assert.Len(t, seen, total, "expected %d distinct IDs from %d goroutines", total, goroutines)
}

func TestNewID_DailyRollover(t *testing.T) {
	root := t.TempDir()
	day1 := time.Date(2026, 4, 7, 23, 59, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 8, 0, 0, 1, 0, time.UTC)

	id1, rel, err := NewIDAt(root, NamespaceMissions, day1)
	require.NoError(t, err)
	rel(true)
	assert.Equal(t, "m-2026-04-07-001", id1)

	id2, rel, err := NewIDAt(root, NamespaceMissions, day1)
	require.NoError(t, err)
	rel(true)
	assert.Equal(t, "m-2026-04-07-002", id2)

	id3, rel, err := NewIDAt(root, NamespaceMissions, day2)
	require.NoError(t, err)
	rel(true)
	assert.Equal(t, "m-2026-04-08-001", id3)

	id4, rel, err := NewIDAt(root, NamespaceMissions, day1)
	require.NoError(t, err)
	rel(true)
	assert.Equal(t, "m-2026-04-07-003", id4)
}

func TestNewID_PadsZeroes(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	for i := 1; i <= 12; i++ {
		id, release, err := NewIDAt(root, NamespaceMissions, now)
		require.NoError(t, err)
		release(true)
		assert.Equal(t, fmt.Sprintf("m-2026-04-07-%03d", i), id)
	}
}

// TestNewID_ReleaseRollback verifies that release(false) decrements
// the counter so a failed allocation does not burn an ID.
func TestNewID_ReleaseRollback(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	id1, rel, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-07-001", id1)
	rel(false) // simulate caller failure — roll back the allocation

	id2, rel, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-07-001", id2, "rolled-back ID must be reused")
	rel(true)
}

// TestNewID_ReleaseIdempotent verifies that a second call to release
// is a no-op — the counter does not double-decrement.
func TestNewID_ReleaseIdempotent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	_, rel, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)
	rel(false)
	rel(false) // second call must not decrement again

	// Counter should now read 0 (decremented once from 1). A fresh
	// allocation lands at 1, not at 0 or -1.
	id, rel2, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)
	rel2(true)
	assert.Equal(t, "m-2026-04-07-001", id)
}

// TestNewID_ReleaseSkipsIfAdvanced verifies the release is a no-op
// when a concurrent caller has already advanced the counter past the
// allocated value. Without this guard, the rollback would clobber the
// other caller's allocation.
func TestNewID_ReleaseSkipsIfAdvanced(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	_, rel1, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)

	id2, rel2, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-07-002", id2)
	rel2(true)

	rel1(false) // counter is at 2; rolling back to 0 would corrupt id2

	id3, rel3, err := NewIDAt(root, NamespaceMissions, now)
	require.NoError(t, err)
	rel3(true)
	assert.Equal(t, "m-2026-04-07-003", id3,
		"counter must not be rolled back when a later allocation has already advanced past it")
}

func TestNewID_RejectsEmptyNamespace(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	_, _, err := NewIDAt(root, "", now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestNewID_RejectsBadNamespace(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	_, _, err := NewIDAt(root, "missions/etc", now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace")
}

func TestNewID_RejectsEmptyRoot(t *testing.T) {
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	_, _, err := NewIDAt("", NamespaceMissions, now)
	require.Error(t, err)
}

// writeCounter drops a raw counter value into the counters/ counter
// file for the given UTC date so a test can simulate a poisoned or
// near-exhausted counter.
func writeCounter(t *testing.T, root, namespace string, day time.Time, value string) {
	t.Helper()
	dir := filepath.Join(root, "counters")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, namespace+"-"+day.UTC().Format("2006-01-02"))
	require.NoError(t, os.WriteFile(path, []byte(value), 0o600))
}

// TestNewID_CounterOverflow asserts that NewID refuses to roll past
// 999 for a given day. The counter is bounded by the m-YYYY-MM-DD-NNN
// regex, so a 4-digit value would produce IDs that fail Validate.
func TestNewID_CounterOverflow(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	writeCounter(t, root, NamespaceMissions, now, "999")

	_, _, err := NewIDAt(root, NamespaceMissions, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside valid range")
}

// TestNewID_PoisonedCounter asserts that NewID detects a counter file
// containing maxInt and refuses to issue a 4-digit ID.
func TestNewID_PoisonedCounter(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	writeCounter(t, root, NamespaceMissions, now, "9223372036854775807")

	_, _, err := NewIDAt(root, NamespaceMissions, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside valid range")
}

// TestNewID_NegativeCounter asserts that NewID rejects a negative
// counter file. After the +1 increment a negative value still rounds
// to a value <1, so the bounds check fires.
func TestNewID_NegativeCounter(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	writeCounter(t, root, NamespaceMissions, now, "-5")

	_, _, err := NewIDAt(root, NamespaceMissions, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside valid range")
}
