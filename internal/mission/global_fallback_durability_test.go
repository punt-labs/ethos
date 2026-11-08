//go:build !windows

package mission

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGlobalFallbackHealthyMutationSyncsOwningDirectory(t *testing.T) {
	for _, operation := range ordinaryMutations {
		t.Run(operation, func(t *testing.T) {
			stores, c := globalFallbackStores(t)
			global, repo := stores[0], stores[1]
			if operation == "close" {
				require.NoError(t, global.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
			}
			if operation == "advance" {
				require.NoError(t, global.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue)))
			}
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			var synced []string
			syncDir = func(dir string) error { synced = append(synced, dir); return real(dir) }
			var out MutationOutcome
			var err error
			stderr := captureStderr(t, func() { out, err = ordinaryMutation(repo, c, operation) })
			require.NoError(t, err)
			assert.Empty(t, out.Warnings, "healthy global fallback is not a durability failure")
			assert.Empty(t, stderr)
			assert.Contains(t, synced, global.missionsDir(), "sync G's destination, not S's ancestors")
			assertOrdinaryMutation(t, global, c, operation)
		})
	}
}

func TestGlobalFallbackInstalledDirectorySyncFailureWarns(t *testing.T) {
	stores, c := globalFallbackStores(t)
	real := syncDir
	t.Cleanup(func() { syncDir = real })
	fired := false
	syncDir = func(dir string) error {
		if dir == stores[0].missionsDir() {
			fired = true
			return errors.New("injected global directory sync failure")
		}
		return real(dir)
	}
	out, err := ordinaryMutation(stores[1], c, "update")
	require.NoError(t, err)
	require.True(t, fired)
	require.Len(t, out.Warnings, 1)
	assert.Contains(t, out.Warnings[0], "injected global directory sync failure")
	assertOrdinaryMutation(t, stores[0], c, "update")
}

func TestGlobalFallbackEventFailureRestoresExactBytes(t *testing.T) {
	for _, rollbackFault := range []bool{false, true} {
		name := "healthy-rollback"
		if rollbackFault {
			name = "rollback-directory-sync-failure"
		}
		t.Run(name, func(t *testing.T) {
			stores, c := globalFallbackStores(t)
			global, repo := stores[0], stores[1]
			path, err := global.ContractPath(c.MissionID)
			require.NoError(t, err)
			original, err := os.ReadFile(path)
			require.NoError(t, err)
			original = append([]byte("# preserve original formatting exactly\n"), original...)
			require.NoError(t, os.WriteFile(path, original, 0o600))
			log, err := global.logPath(c.MissionID)
			require.NoError(t, err)
			require.NoError(t, os.Remove(log))
			require.NoError(t, os.Mkdir(log, 0o700)) // Real open(O_APPEND) failure after authority install.
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			calls := 0
			syncDir = func(dir string) error {
				if dir == global.missionsDir() {
					calls++
					if calls == 2 && rollbackFault {
						return errors.New("injected rollback directory sync failure")
					}
				}
				return real(dir)
			}
			out, err := ordinaryMutation(repo, c, "update")
			require.ErrorContains(t, err, "event append failed")
			assert.Empty(t, out.Warnings, "a failed operation must not emit forward-write success warnings")
			assert.Equal(t, 2, calls, "both forward and rollback must sync G")
			if rollbackFault {
				assert.ErrorContains(t, err, "rollback failed")
				assert.ErrorContains(t, err, "injected rollback directory sync failure")
			} else {
				assert.NotContains(t, err.Error(), "rollback failed")
			}
			after, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, original, after)
		})
	}
}

func TestGlobalFallbackDoesNotAuthorizeArbitraryDestinations(t *testing.T) {
	stores, _ := globalFallbackStores(t)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	require.Error(t, stores[1].writeBytes(outside, []byte("do not install"), false))
	assert.NoFileExists(t, outside, "ownership refusal must happen before installation")
}

func TestGlobalFallbackStagingBelongsToGlobalLayer(t *testing.T) {
	stores, c := globalFallbackStores(t)
	repo := stores[1]
	// Repository staging is deliberately unusable. A legitimate G mutation
	// must not depend on S's staging directory (or its filesystem device).
	require.NoError(t, os.MkdirAll(filepath.Dir(repo.policy.TempDir()), 0o700))
	require.NoError(t, os.WriteFile(repo.policy.TempDir(), []byte("not a directory"), 0o600))
	out, err := ordinaryMutation(repo, c, "update")
	require.NoError(t, err)
	assert.Empty(t, out.Warnings)
	assertOrdinaryMutation(t, stores[0], c, "update")
}
