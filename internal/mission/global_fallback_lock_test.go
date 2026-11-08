//go:build !windows

package mission

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func globalFallbackStores(t *testing.T) ([]*Store, *Contract) {
	t.Helper()
	global := NewStore(t.TempDir())
	c := newContract("m-2026-05-23-903")
	require.NoError(t, global.Create(c))
	stores := []*Store{global}
	for range 2 {
		root := t.TempDir()
		p, err := layout.NewRepository(root, root, layout.RecordBlame)
		require.NoError(t, err)
		stores = append(stores, NewRepositoryStore(p, global.Root()))
	}
	return stores, c
}

func waitFallbackSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mission operation")
	}
}

func assertFallbackBlocked(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Errorf("operation passed held authority lock: %v", err)
		done <- err
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGlobalFallbackMixedStoreLockExclusion(t *testing.T) {
	names := []string{"standalone", "repoA", "repoB"}
	for holder := range names {
		for contender := range names {
			t.Run(names[holder]+"/"+names[contender], func(t *testing.T) {
				stores, c := globalFallbackStores(t)
				entered, release := make(chan struct{}), make(chan struct{})
				holderDone := make(chan error, 1)
				started, done := make(chan struct{}), make(chan error, 1)
				go func() {
					holderDone <- stores[holder].withLock(c.MissionID, func() error {
						close(entered)
						<-release
						return nil
					})
				}()
				t.Cleanup(func() {
					close(release)
					require.NoError(t, <-holderDone)
					require.NoError(t, <-done)
				})
				waitFallbackSignal(t, entered)
				go func() {
					close(started)
					done <- stores[contender].withLock(c.MissionID, func() error { return nil })
				}()
				waitFallbackSignal(t, started)
				assertFallbackBlocked(t, done)
			})
		}
	}
}

func TestGlobalFallbackUpdateCannotResurrectClose(t *testing.T) {
	stores, c := globalFallbackStores(t)
	global, repo := stores[0], stores[1]
	require.NoError(t, global.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
	stale, err := repo.Load(c.MissionID)
	require.NoError(t, err)
	stale.Inputs.References = []string{"metadata edit"}
	validated, release := make(chan struct{}), make(chan struct{})
	repo.persistenceCheck = func(phase string) error {
		if phase == "mission_write" {
			close(validated) // Update has read and validated the open snapshot.
			<-release
		}
		return nil
	}
	updateDone := make(chan error, 1)
	go func() { updateDone <- repo.Update(stale) }()
	waitFallbackSignal(t, validated)
	closeDone := make(chan error, 1)
	go func() { _, err := global.Close(c.MissionID, StatusClosed); closeDone <- err }()
	// On the broken implementation Close commits here, then Update installs its
	// stale open snapshot. Always release Update so the final state exposes it.
	assertFallbackBlocked(t, closeDone)
	close(release)
	require.NoError(t, <-updateDone)
	repo.persistenceCheck = nil
	require.NoError(t, <-closeDone)
	final, err := global.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, final.Status)
	assert.ErrorContains(t, repo.Update(stale), "terminal state")
}

func TestGlobalFallbackLockSelectionFailsClosed(t *testing.T) {
	stores, c := globalFallbackStores(t)
	for _, id := range []string{"", "../" + c.MissionID, ".", "not-a-mission"} {
		err := stores[1].withLock(id, func() error { t.Error("invalid ID reached mutation"); return nil })
		require.Error(t, err)
	}
	s := NewRepositoryStore(layout.Policy{}, stores[0].Root())
	require.Error(t, s.withLock(c.MissionID, func() error { t.Error("invalid policy reached mutation"); return nil }))
	require.Error(t, s.withCreateLock(func() error { t.Error("invalid policy reached create"); return nil }))
	path := stores[1].pathSetFor(c.MissionID, layerRepo).contract
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "missing"), path))
	require.Error(t, stores[1].withLock(c.MissionID, func() error { t.Error("symlink fell back to global"); return nil }))
}

func TestGlobalFallbackMigrationWaitsForAuthority(t *testing.T) {
	for holder := range 3 {
		t.Run([]string{"standalone", "repoA", "repoB"}[holder], func(t *testing.T) {
			stores, c := globalFallbackStores(t)
			repo := stores[1]
			entered, release := make(chan struct{}), make(chan struct{})
			holderDone := make(chan error, 1)
			go func() {
				holderDone <- stores[holder].withLock(c.MissionID, func() error { close(entered); <-release; return nil })
			}()
			waitFallbackSignal(t, entered)
			done := make(chan error, 1)
			go func() {
				_, err := migrateOneMission(stores[0].missionsDir(), repo.repoMissionsDir(), repo.repoRoot, c.MissionID, map[string]struct{}{c.MissionID: {}}, false)
				done <- err
			}()
			assertFallbackBlocked(t, done)
			close(release)
			require.NoError(t, <-holderDone)
			require.NoError(t, <-done)
			_, err := repo.Load(c.MissionID)
			require.NoError(t, err)
		})
	}
}

func TestGlobalFallbackRevalidatesAfterMigration(t *testing.T) {
	stores, c := globalFallbackStores(t)
	repo := stores[1]
	// Hold S's selection fence, migrate, then let a waiting repository Update
	// resolve authority. It must edit the repo copy, not a vanished G snapshot.
	path := repo.MissionLockPath(c.MissionID)
	require.NoError(t, layout.EnsureLocalDir(repo.policy, filepath.Dir(path)))
	release, err := acquireDelegationLockPath(path)
	require.NoError(t, err)
	stale, err := repo.Load(c.MissionID)
	require.NoError(t, err)
	stale.Inputs.References = []string{"after migration"}
	done := make(chan error, 1)
	go func() { done <- repo.Update(stale) }()
	assertFallbackBlocked(t, done)
	// Install the same bytes as the existing migration while holding its locks;
	// this avoids recursively acquiring S's already-held lock in this goroutine.
	globalPath, err := stores[0].ContractPath(c.MissionID)
	require.NoError(t, err)
	b, err := os.ReadFile(globalPath)
	require.NoError(t, err)
	repoPath := repo.pathSetFor(c.MissionID, layerRepo).contract
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o700))
	require.NoError(t, os.WriteFile(repoPath, b, 0o600))
	require.NoError(t, os.Remove(globalPath))
	release()
	require.NoError(t, <-done)
	updated, err := os.ReadFile(repoPath)
	require.NoError(t, err)
	assert.True(t, bytes.Contains(updated, []byte("after migration")))
}

func TestGlobalFallbackCreatePinsRepositoryDestination(t *testing.T) {
	global := NewStore(t.TempDir())
	root := t.TempDir()
	repo := NewStoreWithRoots(root, global.Root())
	c := newContract("m-2026-05-23-904")
	c.Inputs.References = []string{"repository record"}
	checked, release := make(chan struct{}), make(chan struct{})
	paused := false
	repo.persistenceCheck = func(phase string) error {
		if phase == "mission_write" && !paused {
			paused = true
			close(checked)
			<-release
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- repo.Create(c) }()
	waitFallbackSignal(t, checked)
	other := newContract(c.MissionID)
	other.Inputs.References = []string{"independent global record"}
	err := global.Create(other)
	close(release)
	require.NoError(t, err)
	require.NoError(t, <-done)
	loaded, err := global.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, other.Inputs.References, loaded.Inputs.References)
	loaded, err = repo.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, c.Inputs.References, loaded.Inputs.References)
}
