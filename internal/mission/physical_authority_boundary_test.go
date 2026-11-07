//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestPhysicalAuthorityRejectsRedirectedMissionAncestry(t *testing.T) {
	for _, component := range []string{"root", ".punt-labs", "ethos", "missions", "mission"} {
		for _, dangling := range []bool{false, true} {
			t.Run(component+map[bool]string{false: "/existing", true: "/dangling"}[dangling], func(t *testing.T) {
				stores, c := globalFallbackStores(t)
				repo := stores[1]
				path := repo.pathSetFor(c.MissionID, layerRepo).contract
				ancestor := map[string]string{
					"root": repo.repoRoot, ".punt-labs": filepath.Join(repo.repoRoot, ".punt-labs"),
					"ethos": repo.policy.RecordRoot(), "missions": repo.repoMissionsDir(), "mission": filepath.Dir(path),
				}[component]
				require.NoError(t, os.MkdirAll(filepath.Dir(ancestor), 0o700))
				if component == "root" {
					require.NoError(t, os.Remove(ancestor))
				}
				target := filepath.Join(t.TempDir(), "redirected")
				if !dangling {
					require.NoError(t, os.Mkdir(target, 0o751))
				}
				require.NoError(t, os.Symlink(target, ancestor))
				_, err := repo.Load(c.MissionID)
				assert.ErrorContains(t, err, "symlink", "must refuse A, not silently load valid G")
				var out MutationOutcome
				assert.Error(t, repo.writeBytesWithOutcome(path, []byte("untrusted install"), false, &out))
				_, err = os.Lstat(path)
				assert.True(t, os.IsNotExist(err))
			})
		}
	}
}

func TestPhysicalAuthorityDelegationClosePreservesRedirectedTarget(t *testing.T) {
	for _, repositoryTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "external", true: "other repository"}[repositoryTarget], func(t *testing.T) {
			root := t.TempDir()
			const mid, did = "m-2026-05-23-951", "d-2026-05-23-001"
			path, err := WriteDelegationSkeleton(root, mid, did, DelegationSkeleton{Tier: TierB, AgentType: "bwk"})
			require.NoError(t, err)
			owner := NewStore(t.TempDir())
			target := filepath.Join(owner.Root(), "external")
			if repositoryTarget {
				owner = NewStoreWithRoots(t.TempDir(), t.TempDir())
				target = DelegationDir(owner.repoRoot, mid, did)
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
			require.NoError(t, os.Rename(filepath.Dir(path), target))
			targetPath := filepath.Join(target, "record.yaml")
			require.NoError(t, os.Chmod(targetPath, 0o640))
			before, err := os.ReadFile(targetPath)
			require.NoError(t, err)
			info, err := os.Stat(targetPath)
			require.NoError(t, err)
			// Hold both real owner locks, with different canonical inodes. A's
			// close must refuse, not rewrite B/external under A's unrelated lock.
			releaseA, err := NewStoreWithRoots(root, t.TempDir()).AcquireDelegationLock(did)
			require.NoError(t, err)
			defer releaseA()
			release, err := owner.AcquireDelegationLock(did)
			require.NoError(t, err)
			defer release()
			lockInfo, err := os.Stat(owner.DelegationLockPath(did))
			require.NoError(t, err)
			require.NoError(t, os.Symlink(target, filepath.Dir(path)))
			err = CloseDelegationSkeleton(root, mid, did, DelegationVerdictAborted, "2026-05-23T12:00:00Z")
			assert.ErrorContains(t, err, "symlink")
			after, err := os.ReadFile(targetPath)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			afterInfo, err := os.Stat(targetPath)
			require.NoError(t, err)
			assert.Equal(t, info.Mode(), afterInfo.Mode())
			afterLockInfo, err := os.Stat(owner.DelegationLockPath(did))
			require.NoError(t, err)
			assert.True(t, os.SameFile(lockInfo, afterLockInfo), "target lock identity must not change")
			assert.True(t, globalAdmissionFenceHeld(t, owner.DelegationLockPath(did)), "target lock remains held")
		})
	}
}

func TestPhysicalAuthorityLinkedAndAbsentControls(t *testing.T) {
	store, checkout := t.TempDir(), t.TempDir()
	// A .git file is normal for a linked checkout, not a symlink ancestor.
	require.NoError(t, os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: elsewhere\n"), 0o600))
	p, err := layout.NewRepository(store, checkout, layout.RecordFull)
	require.NoError(t, err)
	global := NewStore(t.TempDir())
	c := newContract("m-2026-05-23-952")
	require.NoError(t, global.Create(c))
	repo := NewRepositoryStore(p, global.Root())
	_, err = repo.Load(c.MissionID)
	require.NoError(t, err, "legitimately absent repository mission permits global fallback")
	for _, dest := range []string{filepath.Join(p.RecordRoot(), "control.yaml"), filepath.Join(p.CheckoutRecordRoot(), "control.yaml")} {
		w, err := localReplace(p, dest, []byte("control"))
		require.NoError(t, err)
		assert.NoError(t, w.DurabilityError)
	}
}

func TestPhysicalAuthorityDelegationCloseRefusesBeforeRead(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	const mid, did = "m-2026-05-23-956", "d-2026-05-23-002"
	dir := DelegationDir(root, mid, did)
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o700))
	// A valid target would only prove the install guard. Malformed bytes prove
	// the caller refuses ancestry before attempting to decode redirected data.
	require.NoError(t, os.WriteFile(filepath.Join(target, "record.yaml"), []byte("[not valid yaml"), 0o640))
	require.NoError(t, os.Symlink(target, dir))
	err := CloseDelegationSkeleton(root, mid, did, DelegationVerdictAborted, "2026-05-23T12:00:00Z")
	assert.ErrorContains(t, err, "refusing to follow symlink")
}

func TestPhysicalAuthorityNonDirectoryAndMarkerControls(t *testing.T) {
	for _, component := range []string{"root", ".punt-labs", "ethos", "missions", "mission"} {
		t.Run(component, func(t *testing.T) {
			stores, c := globalFallbackStores(t)
			repo := stores[1]
			path := repo.pathSetFor(c.MissionID, layerRepo).contract
			ancestor := map[string]string{
				"root": repo.repoRoot, ".punt-labs": filepath.Join(repo.repoRoot, ".punt-labs"),
				"ethos": repo.policy.RecordRoot(), "missions": repo.repoMissionsDir(), "mission": filepath.Dir(path),
			}[component]
			require.NoError(t, os.MkdirAll(filepath.Dir(ancestor), 0o700))
			if component == "root" {
				require.NoError(t, os.Remove(ancestor))
			}
			require.NoError(t, os.WriteFile(ancestor, []byte("not a directory"), 0o600))
			_, err := repo.Load(c.MissionID)
			assert.ErrorContains(t, err, "not a directory")
		})
	}
	// The caller's root may itself have a .punt-labs marker in its ancestry.
	// Matching the first marker would adopt the wrong policy root.
	root := filepath.Join(t.TempDir(), ".punt-labs", "nested", "repository")
	require.NoError(t, os.MkdirAll(root, 0o700))
	repo := NewStoreWithRoots(root, t.TempDir())
	c := newContract("m-2026-05-23-957")
	require.NoError(t, repo.Create(c))
	_, err := repo.Load(c.MissionID)
	require.NoError(t, err)
}

func TestPhysicalAuthorityDelegationCreationRefusesBeforeMkdir(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	const mid, did = "m-2026-05-23-959", "d-2026-05-23-003"
	dir := DelegationDir(root, mid, did)
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Dir(dir)), 0o700))
	require.NoError(t, os.Symlink(target, filepath.Dir(dir)))
	_, err := WriteDelegationSkeleton(root, mid, did, DelegationSkeleton{Tier: TierB, AgentType: "bwk"})
	assert.ErrorContains(t, err, "symlink")
	entries, err := os.ReadDir(target)
	require.NoError(t, err)
	assert.Empty(t, entries, "refusal precedes creation of even an empty target delegation directory")
}

func TestPhysicalAuthorityDelegationConvenienceLocations(t *testing.T) {
	const did = "d-2026-05-23-004"
	for _, path := range []string{
		filepath.Join(t.TempDir(), "record.yaml"),
		filepath.Join(t.TempDir(), ".punt-labs", "ethos", "delegations", did, "record.yaml"),
		filepath.Join(t.TempDir(), "exported-delegation.yaml"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("id: "+did+"\nverdict: open\n"), 0o600))
		_, err := LoadDelegation(path)
		require.NoError(t, err)
		require.NoError(t, CloseDelegation(path, DelegationVerdictPass, ""))
	}
}
