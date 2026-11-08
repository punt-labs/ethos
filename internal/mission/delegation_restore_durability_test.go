package mission

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

func TestDisclaimRestoreDurability(t *testing.T) {
	for _, failRestore := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy restore", true: "second directory sync fails"}[failRestore], func(t *testing.T) {
			home, repo := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			s := NewStoreWithRoots(repo, t.TempDir())
			c := newContract("m-2026-09-08-818")
			require.NoError(t, s.Create(c))
			const did = "d-2026-09-08-016"
			path, err := WriteDelegationSkeleton(repo, c.MissionID, did, DelegationSkeleton{
				Tier: TierB, AgentType: c.Worker, BoundVia: BoundViaSidecarDispatch,
			})
			require.NoError(t, err)
			require.NoError(t, CloseDelegationSkeleton(repo, c.MissionID, did, DelegationVerdictPass, c.CreatedAt))
			d, err := LoadDelegation(path)
			require.NoError(t, err)
			d.Session = filepath.Join(home, "legacy", "session")
			before, err := yaml.Marshal(d)
			require.NoError(t, err)
			before = append([]byte("# preserve this exact legacy representation\n"), before...)
			require.NoError(t, os.WriteFile(path, before, 0o600))
			log := audit.LiveMissionLogPath(repo, c.MissionID, sessionlessID)
			eventsBefore, err := os.ReadFile(log)
			require.NoError(t, err)
			require.NoError(t, os.Remove(log))
			require.NoError(t, os.Mkdir(log, 0o700)) // required append must fail
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			calls := 0
			syncDir = func(dir string) error {
				if dir == filepath.Dir(path) {
					calls++
					if calls == 2 && failRestore {
						return errors.New("injected restore directory sync failure")
					}
				}
				return real(dir)
			}
			var result *Delegation
			notice := captureStderr(t, func() { result, err = s.DisclaimDelegation(c.MissionID, did, "unrelated capture") })
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Equal(t, 2, calls, "forward install succeeds before restore's separate durability check")
			if failRestore {
				assert.ErrorContains(t, err, "rollback failed")
				assert.ErrorContains(t, err, "injected restore directory sync failure")
				assert.NotContains(t, err.Error(), "record rolled back")
			} else {
				assert.ErrorContains(t, err, "record rolled back")
				assert.NotContains(t, err.Error(), "rollback failed")
			}
			assert.Empty(t, notice, "failed transactions do not announce a successful mutation or restore")
			after, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, before, after, "restoration bypasses the guard, even with comments and raw HOME")
			require.NoError(t, os.Remove(log))
			require.NoError(t, os.WriteFile(log, eventsBefore, 0o600))
			syncDir = real
			_, err = s.DisclaimDelegation(c.MissionID, did, "unrelated capture")
			require.NoError(t, err)
			events, warnings, err := s.LoadEvents(c.MissionID)
			require.NoError(t, err)
			assert.Empty(t, warnings)
			count := 0
			for _, event := range events {
				if event.Event == "disclaim_delegation" {
					count++
				}
			}
			assert.Equal(t, 1, count, "retry must not duplicate an event from the refused attempt")
		})
	}
}
