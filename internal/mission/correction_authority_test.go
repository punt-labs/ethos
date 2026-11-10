package mission

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestCorrectionAuthorityMissingV1RefusesBeforePreparation(t *testing.T) {
	for _, history := range []string{"no-events", "unrelated-events"} {
		t.Run(history, func(t *testing.T) {
			s, c := authorityStore(t, 2)
			require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
			_, err := s.Close(c.MissionID, StatusClosed)
			require.NoError(t, err)
			path := s.correctionsPath(c.MissionID)
			require.NoError(t, os.Remove(path))
			stream := audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "original"}
			live := stream.LivePath(s.policy)
			if history == "no-events" {
				require.NoError(t, os.Remove(live))
			} // Otherwise the genuine create/result/close events remain, none correct.
			var before []byte
			if history == "unrelated-events" {
				before, err = os.ReadFile(live)
				require.NoError(t, err)
			}
			contractPath := mustContractPath(t, s, c.MissionID)
			contractBefore, err := os.ReadFile(contractPath)
			require.NoError(t, err)
			prepared := false
			s.persistenceCheck = func(phase string) error {
				if phase == "event_prepared" {
					prepared = true
				}
				return nil
			}
			_, _, err = s.LoadCorrections(c.MissionID)
			require.ErrorContains(t, err, "corrections authority unavailable")
			err = s.Correct(c.MissionID, Correction{Mission: c.MissionID, Author: "claude", Kind: CorrectionDecision, Corrected: "new decision"})
			require.ErrorContains(t, err, "corrections authority unavailable; missing is not an initialized empty history")
			assert.False(t, prepared)
			assert.NoFileExists(t, path)
			assert.NoFileExists(t, s.correctionIntentPath(c.MissionID))
			if history == "no-events" {
				assert.NoFileExists(t, live, "refusal must not prepare or append an event")
			} else {
				after, err := os.ReadFile(live)
				require.NoError(t, err)
				assert.Equal(t, before, after)
			}
			contractAfter, err := os.ReadFile(contractPath)
			require.NoError(t, err)
			assert.Equal(t, contractBefore, contractAfter)
		})
	}
}

func TestCorrectionAuthorityInterruptedRetryPreservesConsent(t *testing.T) {
	s, c := authorityStore(t, 2) // Original consent is blame.
	require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
	_, err := s.Close(c.MissionID, StatusClosed)
	require.NoError(t, err)
	s.persistenceCheck = func(phase string) error {
		if phase == "event_prepared" {
			return errors.New("interrupt before required append")
		}
		return nil
	}
	correction := Correction{Mission: c.MissionID, Author: "claude", Kind: CorrectionDecision, Corrected: "new decision"}
	require.Error(t, s.Correct(c.MissionID, correction))
	in, err := s.readCorrectionIntent(c.MissionID)
	require.NoError(t, err)
	require.NotNil(t, in)
	assert.Equal(t, "blame", in.Event.Record)
	p := s.policy
	// A copied policy cannot be mutated; construct the next request's snapshot.
	full, err := layout.NewRepository(s.repoRoot, s.checkoutRoot, layout.RecordFull)
	require.NoError(t, err)
	retry := NewRepositoryStore(full, s.root).WithSessionID("retry")
	require.NoError(t, retry.Correct(c.MissionID, correction))
	events, warnings, err := retry.LoadEvents(c.MissionID)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	count := 0
	for _, event := range events {
		if event.OperationID == in.Entry.ID {
			count++
			assert.Equal(t, in.Event.TS, event.TS)
			assert.Equal(t, "blame", event.Record)
		}
	}
	assert.Equal(t, 1, count)
	assert.NoFileExists(t, (audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "retry"}).LivePath(p))
	assert.NoFileExists(t, retry.correctionIntentPath(c.MissionID))
	assert.NoDirExists(t, filepath.Join(p.CheckoutRecordRoot(), "missions", c.MissionID))
}

// A refusal states the condition and never tells the reader to act: the text
// can reach a residual reason that doctor shows.
func TestPendingPreparationMessagesStateTheCondition(t *testing.T) {
	s, c := authorityStore(t, 2)
	require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
	_, err := s.Close(c.MissionID, StatusClosed)
	require.NoError(t, err)
	s.persistenceCheck = func(phase string) error {
		if phase == "event_prepared" {
			return errors.New("interrupt before required append")
		}
		return nil
	}
	require.Error(t, s.Correct(c.MissionID, Correction{Mission: c.MissionID, Author: "claude", Kind: CorrectionDecision, Corrected: "new decision"}))
	s.persistenceCheck = nil
	err = s.reconcileCorrection(c.MissionID)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "retry")

	s, c = authorityStore(t, 3)
	s.persistenceCheck = func(phase string) error {
		if phase == "event_prepared" {
			return errors.New("interrupt before required append")
		}
		return nil
	}
	_, err = s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
	require.Error(t, err)
	s.persistenceCheck = nil
	err = s.reconcileReflection(c.MissionID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pending reflection preparation for round 1")
	assert.NotContains(t, err.Error(), "retry")
}
