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

func TestGlobalResultRemovalDurability(t *testing.T) {
	for _, failRemovalSync := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "removal sync failure"}[failRemovalSync], func(t *testing.T) {
			stores, c := globalFallbackStores(t)
			global, repo := stores[0], stores[1]
			log, err := global.logPath(c.MissionID)
			require.NoError(t, err)
			before, err := os.ReadFile(log)
			require.NoError(t, err)
			savedLog := log + ".saved"
			require.NoError(t, os.Rename(log, savedLog))
			require.NoError(t, os.Mkdir(log, 0o700)) // Actual required-event open failure.
			resultPath := global.pathSetFor(c.MissionID, layerGlobal).results
			require.NoFileExists(t, resultPath)
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			calls := 0
			syncDir = func(dir string) error {
				if dir == global.missionsDir() {
					calls++
					if calls == 2 && failRemovalSync {
						return errors.New("injected result removal sync failure")
					}
				}
				return real(dir)
			}
			out, err := repo.AppendResultWithOutcome(c.MissionID, resultFor(c, VerdictPass))
			require.ErrorContains(t, err, "event append failed")
			assert.Empty(t, out.Warnings)
			assert.Equal(t, 2, calls, "forward install and absent-file restore both sync G")
			if failRemovalSync {
				assert.ErrorContains(t, err, "rollback")
				assert.ErrorContains(t, err, "injected result removal sync failure")
			} else {
				assert.NotContains(t, err.Error(), "rollback sync failed")
				assert.NotContains(t, err.Error(), "outside the canonical")
			}
			assert.NoFileExists(t, resultPath)
			after, readErr := os.ReadFile(savedLog)
			require.NoError(t, readErr)
			assert.Equal(t, before, after)
			entries, readErr := os.ReadDir(log)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "failed open must not append any event")
			assert.Equal(t, filepath.Dir(resultPath), global.missionsDir())
		})
	}
}

func TestGlobalResultRemovalRefusesUnrecognizedOwner(t *testing.T) {
	stores, _ := globalFallbackStores(t)
	for _, path := range []string{
		filepath.Join(t.TempDir(), "outside.yaml"),
		filepath.Join(stores[0].missionsDir(), "arbitrary.yaml"),
		filepath.Join(stores[0].missionsDir(), "m-2026-05-23-903", "nested.yaml"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("preserve"), 0o640))
		require.Error(t, stores[1].removeBytes(path))
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "preserve", string(b))
	}
}
