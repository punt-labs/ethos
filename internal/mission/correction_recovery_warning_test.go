package mission

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCorrectionPriorAcceptedRecoveryWarning(t *testing.T) {
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
			if !refuse {
				_, _, err := s.AbandonWithOutcome(c.MissionID, "cancelled")
				require.NoError(t, err)
			}
			correction := Correction{Mission: c.MissionID, Author: "claude", Kind: CorrectionDecision, Corrected: "new annotation"}
			var out MutationOutcome
			var err error
			stderr := captureStderr(t, func() { out, err = s.CorrectWithOutcome(c.MissionID, correction) })
			assert.Empty(t, stderr)
			if refuse {
				require.ErrorContains(t, err, "is open")
			} else {
				require.NoError(t, err)
			}
			require.Len(t, out.Warnings, 1)
			assert.Contains(t, out.Warnings[0], "round 1 accepted; optional reflection trace pending")
			corrections, _, err := s.LoadCorrections(c.MissionID)
			require.NoError(t, err)
			if refuse {
				assert.Empty(t, corrections)
			} else {
				require.Equal(t, []Correction{correction}, corrections)
			}
			stderr = captureStderr(t, func() { err = s.Correct(c.MissionID, correction) })
			assert.Equal(t, 1, strings.Count(stderr, "accepted; optional reflection trace pending"))
		})
	}
}

func TestCorrectionPriorAcceptedWarningRequiredFailure(t *testing.T) {
	s, c := authorityStore(t, 3)
	retainAcceptedReflection(t, s, c)
	s.persistenceCheck = func(phase string) error {
		if phase == "reflect_archive" {
			return os.ErrPermission
		}
		return nil
	}
	_, _, err := s.AbandonWithOutcome(c.MissionID, "cancelled")
	require.NoError(t, err)
	required := errors.New("current required correction event interrupted")
	s.persistenceCheck = func(phase string) error {
		if phase == "reflect_archive" {
			return os.ErrPermission
		}
		if phase == "event_prepared" {
			return required
		}
		return nil
	}
	out, err := s.CorrectWithOutcome(c.MissionID, Correction{Mission: c.MissionID, Author: "claude", Kind: CorrectionDecision, Corrected: "new annotation"})
	require.ErrorIs(t, err, required)
	require.Len(t, out.Warnings, 1)
	assert.Contains(t, out.Warnings[0], "round 1 accepted; optional reflection trace pending")
	assert.NotContains(t, out.Warnings[0], "current required")
	corrections, _, err := s.LoadCorrections(c.MissionID)
	require.NoError(t, err)
	assert.Empty(t, corrections)
	assert.FileExists(t, s.correctionIntentPath(c.MissionID))
}

func TestReflectionRecoveryCompatibilityWarningOnce(t *testing.T) {
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
	stderr := captureStderr(t, func() {
		err = s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 3, RecommendationContinue))
	})
	require.Error(t, err)
	assert.Equal(t, 1, strings.Count(stderr, "accepted; optional reflection trace pending"))
}
