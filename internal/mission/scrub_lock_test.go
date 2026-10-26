//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestScrub_DelegationRecordSerializesWithTheCloseSweep: scrub reads an open
// delegation record, and a close sweep (which runs under the exclusive repo
// mission lock) must not close it before scrub writes: the stale open snapshot
// would then overwrite the close. Scrub holds the shared mission lock, as the
// sweep's order requires, so the sweep waits and the record ends up closed.
func TestScrub_DelegationRecordSerializesWithTheCloseSweep(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo, global := t.TempDir(), t.TempDir()
	const mid, did = "m-2026-09-08-860", "d-2026-09-08-860"
	recordPath, err := WriteDelegationSkeleton(repo, mid, did, DelegationSkeleton{
		Tier: TierB, AgentType: "bwk", BoundVia: BoundViaSidecarDispatch,
	})
	require.NoError(t, err)
	// The record as an older ethos left it: open, and naming the home.
	d, err := LoadDelegation(recordPath)
	require.NoError(t, err)
	d.Session = filepath.Join(home, "old", "session")
	raw, err := yaml.Marshal(d)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(recordPath, raw, 0o600))

	atWrite, release := make(chan struct{}), make(chan struct{})
	orig := scrubWrite
	scrubWrite = func(dir, pattern, dest string, data []byte) error {
		close(atWrite)
		<-release
		return orig(dir, pattern, dest, data)
	}
	t.Cleanup(func() { scrubWrite = orig })

	scrubDone := make(chan error, 1)
	go func() {
		_, err := ScrubFiles(repo, global, []string{".punt-labs/ethos/missions/" + mid + "/delegations/" + did + "/record.yaml"}, false)
		scrubDone <- err
	}()
	<-atWrite // scrub has read the open record and is about to write it

	closerDone := make(chan struct{})
	go func() {
		defer close(closerDone)
		unlock, lerr := AcquireMissionLockExclusive(repo, mid)
		if lerr == nil {
			closeDelegationSkeletons(repo, mid, DelegationVerdictAborted, "2026-09-08T12:00:00Z")
			unlock()
		}
	}()
	select {
	case <-closerDone:
		// Without the lock the close ran inside scrub's read-to-write window.
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-scrubDone)
	<-closerDone

	got, err := LoadDelegation(recordPath)
	require.NoError(t, err)
	assert.Equal(t, DelegationVerdictAborted, got.Verdict, "the close must not be undone by a stale scrub write")
	assert.NotContains(t, got.Session, home, "and the record is scrubbed")
}

// snapshotTree maps every path under root to "dir" or to the file's content,
// so two snapshots differ when a path is created or removed or a file's
// content changes.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			snap[p] = "dir"
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		snap[p] = string(b)
		return nil
	}))
	return snap
}

// TestScrub_DryRunAndAbsentMissionTakeNoLocks: a dry run writes nothing, and
// neither does a scrub of a record whose mission directory is gone. Both leave
// the repo and the global store as they were: no lock file, no directory.
func TestScrub_DryRunAndAbsentMissionTakeNoLocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo, global := t.TempDir(), t.TempDir()
	const mid, did = "m-2026-09-08-861", "d-2026-09-08-861"
	recordPath, err := WriteDelegationSkeleton(repo, mid, did, DelegationSkeleton{
		Tier: TierB, AgentType: "bwk", BoundVia: BoundViaSidecarDispatch,
	})
	require.NoError(t, err)
	d, err := LoadDelegation(recordPath)
	require.NoError(t, err)
	d.Session = filepath.Join(home, "old", "session")
	raw, err := yaml.Marshal(d)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(recordPath, raw, 0o600))
	rel := ".punt-labs/ethos/missions/" + mid + "/delegations/" + did + "/record.yaml"

	t.Run("dry run", func(t *testing.T) {
		beforeRepo, beforeGlobal := snapshotTree(t, repo), snapshotTree(t, global)
		res, err := ScrubFiles(repo, global, []string{rel}, true)
		require.NoError(t, err)
		assert.Equal(t, []string{rel}, res.Changed, "the dry run still reports the change")
		assert.Equal(t, beforeRepo, snapshotTree(t, repo))
		assert.Equal(t, beforeGlobal, snapshotTree(t, global))
	})

	t.Run("absent mission directory", func(t *testing.T) {
		gone := ".punt-labs/ethos/missions/m-2026-09-08-862/delegations/d-2026-09-08-862/record.yaml"
		beforeRepo, beforeGlobal := snapshotTree(t, repo), snapshotTree(t, global)
		res, err := ScrubFiles(repo, global, []string{gone}, false)
		require.NoError(t, err)
		assert.Equal(t, []string{gone}, res.Skipped)
		assert.Equal(t, beforeRepo, snapshotTree(t, repo))
		assert.Equal(t, beforeGlobal, snapshotTree(t, global))
	})
}
