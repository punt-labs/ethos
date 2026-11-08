package mission

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReflectionPriorAcceptedRecoveryWarning(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "refusal"}[refuse], func(t *testing.T) {
			s, c := authorityStore(t, 3)
			retainAcceptedReflection(t, s, c)
			s.persistenceCheck = func(phase string) error {
				if phase == "reflect_archive" {
					return os.ErrPermission
				}
				return nil
			}
			_, _, err := s.AdvanceRoundWithOutcome(c.MissionID, c.Leader)
			require.NoError(t, err)
			round := 2
			if refuse {
				round = 3
			}
			var out OperationResult
			stderr := captureStderr(t, func() {
				out, err = s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, round, RecommendationContinue))
			})
			assert.Empty(t, stderr, "explicit outcome owns diagnostics")
			if refuse {
				require.ErrorContains(t, err, "does not match")
				assert.Equal(t, "not_applied", out.Application)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "applied", out.Application)
			}
			require.NotEmpty(t, out.Diagnostics)
			assert.Contains(t, out.Diagnostics[0], "round 1 accepted; optional reflection trace pending")
			assert.FileExists(t, s.intentPath(c.MissionID, 1))
		})
	}
}

func TestReflectionPriorAcceptedWarningRequiredFailure(t *testing.T) {
	s, c := authorityStore(t, 3)
	retainAcceptedReflection(t, s, c)
	_, _, err := s.AdvanceRoundWithOutcome(c.MissionID, c.Leader)
	require.NoError(t, err)
	// Restore the accepted receipt after advancement completed its archive.
	in, err := readIntent(s.reflectionArchivePath(c.MissionID, 1), c.MissionID)
	require.NoError(t, err)
	require.NotNil(t, in)
	require.NoError(t, s.persistIntent(*in))
	required := errors.New("current required event interrupted")
	s.persistenceCheck = func(phase string) error {
		if phase == "reflect_archive" {
			return os.ErrPermission
		}
		if phase == "event_prepared" {
			return required
		}
		return nil
	}
	out, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 2, RecommendationContinue))
	require.ErrorIs(t, err, required)
	assert.Equal(t, "not_applied", out.Application)
	require.Len(t, out.Diagnostics, 1)
	assert.Contains(t, out.Diagnostics[0], "round 1 accepted; optional reflection trace pending")
	after, err := s.Load(c.MissionID)
	require.NoError(t, err)
	require.Len(t, after.RoundDecisions.Entries, 1)
	assert.FileExists(t, s.intentPath(c.MissionID, 2), "unaccepted preparation remains classified separately")
}
