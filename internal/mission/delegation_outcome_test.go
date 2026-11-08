package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelegationOutcomeCloseInstalledDurability(t *testing.T) {
	for _, compatibility := range []bool{false, true} {
		t.Run(fmt.Sprint(compatibility), func(t *testing.T) {
			s, c := prepareOrdinaryMutation(t, "close")
			path, err := WriteDelegationSkeletonWithPolicy(s.policy, c.MissionID, "d-outcome", DelegationSkeleton{Tier: TierB, AgentType: c.Worker})
			require.NoError(t, err)
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			failed := false
			syncDir = func(dir string) error {
				if dir == filepath.Dir(path) {
					failed = true
					return errors.New("delegation directory sync failed")
				}
				return real(dir)
			}
			var out MutationOutcome
			stderr := captureStderr(t, func() {
				if compatibility {
					_, err = s.Close(c.MissionID, StatusClosed)
				} else {
					_, out, err = s.CloseWithOutcome(c.MissionID, StatusClosed)
				}
			})
			require.NoError(t, err)
			require.True(t, failed, "fault must reach only the delegation directory")
			if compatibility {
				assert.Equal(t, 1, strings.Count(stderr, "update installed; durability unconfirmed"))
			} else {
				assert.Empty(t, stderr, "outcome API owns the warning")
				require.Len(t, out.Warnings, 1)
				assert.Contains(t, out.Warnings[0], "update installed; durability unconfirmed")
				assert.Contains(t, out.Warnings[0], "delegation directory sync failed")
			}
			loaded, err := s.Load(c.MissionID)
			require.NoError(t, err)
			assert.Equal(t, StatusClosed, loaded.Status)
			events, warnings, err := s.LoadEvents(c.MissionID)
			require.NoError(t, err)
			assert.Empty(t, warnings)
			require.NotEmpty(t, events)
			assert.Equal(t, "close", events[len(events)-1].Event)
			d, err := LoadDelegation(path)
			require.NoError(t, err)
			assert.Equal(t, DelegationVerdictPass, d.Verdict)
			assert.NotEmpty(t, d.ClosedAt)
			assert.GreaterOrEqual(t, d.ClosedAt, loaded.ClosedAt)
		})
	}
}

func TestDelegationOutcomeCompatibilityCloseOnce(t *testing.T) {
	s, c := authorityStore(t, 3)
	path, err := WriteDelegationSkeletonWithPolicy(s.policy, c.MissionID, "d-outcome", DelegationSkeleton{Tier: TierB})
	require.NoError(t, err)
	real := syncDir
	t.Cleanup(func() { syncDir = real })
	syncDir = func(dir string) error {
		if dir == filepath.Dir(path) {
			return errors.New("delegation directory sync failed")
		}
		return real(dir)
	}
	stderr := captureStderr(t, func() {
		err = CloseDelegationSkeleton(s.repoRoot, c.MissionID, "d-outcome", DelegationVerdictAborted, c.CreatedAt)
	})
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(stderr, "update installed; durability unconfirmed"))
}

func TestDelegationOutcomePreinstallFailure(t *testing.T) {
	s, c := authorityStore(t, 3)
	path, err := WriteDelegationSkeletonWithPolicy(s.policy, c.MissionID, "d-outcome", DelegationSkeleton{Tier: TierB})
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.Remove(s.policy.TempDir()))
	require.NoError(t, os.WriteFile(s.policy.TempDir(), []byte("blocked staging"), 0o600))
	var out MutationOutcome
	stderr := captureStderr(t, func() {
		err = closeDelegationSkeletonWithOutcome(s.repoRoot, c.MissionID, "d-outcome", DelegationVerdictAborted, c.CreatedAt, &out)
	})
	require.Error(t, err)
	assert.Empty(t, out.Warnings)
	assert.Empty(t, stderr)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
