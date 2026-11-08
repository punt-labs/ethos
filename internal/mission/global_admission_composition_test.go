//go:build !windows

package mission

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A nonblocking probe on a separate descriptor observes the actual kernel
// fence, rather than inferring lock ownership from a goroutine's silence.
func globalAdmissionFenceHeld(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true
	}
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_UN))
	return false
}

func TestGlobalAdmissionMixedForceReleaseRollback(t *testing.T) {
	for _, repoRelease := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone release repository create", true: "repository release standalone create"}[repoRelease], func(t *testing.T) {
			stores, c := globalFallbackStores(t)
			global, repo := stores[0], stores[1]
			// Repository admission must actually include this G mission.
			audit := filepath.Join(repo.repoRoot, ".punt-labs", "local", "ethos", "sessions", "owned.audit.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(audit), 0o700))
			require.NoError(t, os.WriteFile(audit, []byte("{\"contract_id\":\""+c.MissionID+"\"}\n"), 0o600))
			releaser, creator := global, repo
			if repoRelease {
				releaser, creator = repo, global
			}
			// Both event implementations eventually open this G log.
			log := global.pathSetFor(c.MissionID, layerGlobal).log
			require.NoError(t, os.Remove(log))
			require.NoError(t, os.Mkdir(log, 0o700))
			installed, finishRelease := make(chan struct{}), make(chan struct{})
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			var paused atomic.Bool
			syncDir = func(dir string) error {
				if dir == global.missionsDir() && paused.CompareAndSwap(false, true) {
					close(installed) // WriteSetReleasedAt is installed; required event has not opened.
					<-finishRelease
				}
				return real(dir)
			}
			releaseDone := make(chan error, 1)
			go func() { _, err := releaser.ForceReleaseWriteSet(c.MissionID, "explicit release"); releaseDone <- err }()
			waitFallbackSignal(t, installed)
			current, err := global.Load(c.MissionID)
			require.NoError(t, err)
			require.NotEmpty(t, current.WriteSetReleasedAt)
			assert.True(t, globalAdmissionFenceHeld(t, global.createLockPath()), "temporary global release must hold G admission fence")
			attempt := make(chan struct{})
			var signalled atomic.Bool
			creator.persistenceCheck = func(phase string) error {
				if phase == "mission_create_lock_wait:"+creator.createLockPath() && signalled.CompareAndSwap(false, true) {
					close(attempt)
				}
				return nil
			}
			other := newContract("m-2026-05-23-953")
			createDone := make(chan error, 1)
			go func() { createDone <- creator.Create(other) }()
			waitFallbackSignal(t, attempt)
			// Actual entry was attempted and the separate probe above checks the
			// shared fence. The bounded wait also exposes the old admitted state.
			var createErr error
			finished := false
			select {
			case createErr = <-createDone:
				finished = true
			case <-time.After(150 * time.Millisecond):
			}
			close(finishRelease)
			require.ErrorContains(t, <-releaseDone, "event append failed")
			if !finished {
				createErr = <-createDone
			}
			assert.ErrorContains(t, createErr, "conflict")
			final, err := global.Load(c.MissionID)
			require.NoError(t, err)
			assert.Empty(t, final.WriteSetReleasedAt)
			_, err = creator.Load(other.MissionID)
			assert.Error(t, err, "no conflicting mission may survive rollback")
		})
	}
}

func TestGlobalAdmissionCreatePinsBeforeDestinationSelection(t *testing.T) {
	global := NewStore(t.TempDir())
	repo := NewStoreWithRoots(t.TempDir(), global.Root())
	migrator := NewStoreWithRoots(t.TempDir(), global.Root())
	c := newContract("m-2026-05-23-954")
	selected, resumeSelection := make(chan struct{}), make(chan struct{})
	destination, resumeDestination := make(chan struct{}), make(chan struct{})
	repo.persistenceCheck = func(phase string) error {
		switch phase {
		case "mission_authority_selected":
			close(selected)
			<-resumeSelection
		case "mission_create_destination":
			close(destination)
			<-resumeDestination
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- repo.Create(c) }()
	waitFallbackSignal(t, selected) // A selected absent without a G state lock.
	require.NoError(t, global.Create(newContract(c.MissionID)))
	close(resumeSelection)
	// A correct implementation may refuse the now-present duplicate. If it
	// proceeds, migration removes G after its destination was selected.
	select {
	case err := <-done:
		require.ErrorContains(t, err, "already exists")
		return
	case <-destination:
	case <-time.After(5 * time.Second):
		t.Fatal("create never reached destination check")
	}
	_, err := migrateOneMission(global.missionsDir(), migrator.repoMissionsDir(), migrator.repoRoot, c.MissionID, map[string]struct{}{c.MissionID: {}}, false)
	require.NoError(t, err)
	close(resumeDestination)
	err = <-done
	if err != nil {
		require.ErrorContains(t, err, "already exists")
	} else {
		assert.FileExists(t, repo.pathSetFor(c.MissionID, layerRepo).contract)
	}
	assert.NoFileExists(t, global.pathSetFor(c.MissionID, layerGlobal).contract, "repository create must never install to its unlocked G snapshot")
}

func TestGlobalAdmissionRevalidatesActualGlobalWait(t *testing.T) {
	for _, initiallyPresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent to present", true: "removed by other repository migration"}[initiallyPresent], func(t *testing.T) {
			global := NewStore(t.TempDir())
			repo := NewStoreWithRoots(t.TempDir(), global.Root())
			c := newContract("m-2026-05-23-955")
			if initiallyPresent {
				require.NoError(t, global.Create(c))
			}
			require.NoError(t, os.MkdirAll(global.missionsDir(), 0o700))
			release, err := acquireDelegationLockPath(repo.globalLockPath(c.MissionID))
			require.NoError(t, err)
			attempt := make(chan struct{})
			repo.persistenceCheck = func(phase string) error {
				if phase == "mission_lock_wait:"+repo.globalLockPath(c.MissionID) {
					close(attempt)
				}
				return nil
			}
			done := make(chan error, 1)
			go func() { done <- repo.Update(c) }()
			waitFallbackSignal(t, attempt) // S is held; G lock really is next.
			if initiallyPresent {
				// Run the existing migration body under its S/create and the G
				// authority lock held here; the public wrapper would reacquire G.
				m := NewStoreWithRoots(t.TempDir(), global.Root())
				err = m.withCreateLock(func() error {
					return m.withStateFileLock(m.MissionLockPath(c.MissionID), true, func() error {
						_, err := migrateOneMissionLocked(global.missionsDir(), m.repoMissionsDir(), m.repoRoot, c.MissionID, map[string]struct{}{c.MissionID: {}}, false)
						return err
					})
				})
				require.NoError(t, err)
			} else {
				// Install a valid standalone snapshot under the G lock already held.
				require.NoError(t, global.writeContract(c))
			}
			release()
			err = <-done
			if initiallyPresent {
				assert.Error(t, err)
				assert.NoFileExists(t, global.pathSetFor(c.MissionID, layerGlobal).contract)
			} else {
				assert.NoError(t, err)
				_, err = global.Load(c.MissionID)
				assert.NoError(t, err)
			}
		})
	}
}

func TestGlobalAdmissionRepositoryOnlyDoesNotAcquireGlobalFence(t *testing.T) {
	global := NewStore(t.TempDir())
	require.NoError(t, os.MkdirAll(global.missionsDir(), 0o700))
	// A directory cannot be opened as an admission lock. Repository authority
	// with no fallback scan must not depend on this unusable G fence.
	require.NoError(t, os.Mkdir(global.createLockPath(), 0o700))
	repo := NewStoreWithRoots(t.TempDir(), global.Root())
	c := newContract("m-2026-05-23-958")
	require.NoError(t, repo.Create(c))
	_, err := repo.ForceReleaseWriteSet(c.MissionID, "repository-only release")
	require.NoError(t, err)
	_, err = repo.Load(c.MissionID)
	require.NoError(t, err)
}
