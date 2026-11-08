package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var ordinaryMutations = []string{"update", "result", "advance", "close", "abandon"}

func prepareOrdinaryMutation(t *testing.T, operation string) (*Store, *Contract) {
	t.Helper()
	s, c := authorityStore(t, 3)
	if operation == "close" {
		require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
	}
	return s, c
}

func ordinaryMutation(s *Store, c *Contract, operation string) (MutationOutcome, error) {
	switch operation {
	case "update":
		loaded, err := s.Load(c.MissionID)
		if err != nil {
			return MutationOutcome{}, err
		}
		loaded.Inputs.References = []string{"updated reference"}
		return s.UpdateWithOutcome(loaded)
	case "result":
		return s.AppendResultWithOutcome(c.MissionID, resultFor(c, VerdictPass))
	case "advance":
		_, out, err := s.AdvanceRoundWithOutcome(c.MissionID, c.Leader)
		return out, err
	case "close":
		_, out, err := s.CloseWithOutcome(c.MissionID, StatusClosed)
		return out, err
	case "abandon":
		_, out, err := s.AbandonWithOutcome(c.MissionID, "cancelled before dispatch")
		return out, err
	default:
		return MutationOutcome{}, errors.New("unknown test mutation")
	}
}

func assertOrdinaryMutation(t *testing.T, s *Store, c *Contract, operation string) {
	t.Helper()
	after, err := s.Load(c.MissionID)
	require.NoError(t, err)
	switch operation {
	case "update":
		assert.Equal(t, []string{"updated reference"}, after.Inputs.References)
	case "result":
		r, err := s.LoadResult(c.MissionID, 1)
		require.NoError(t, err)
		require.NotNil(t, r)
		assert.Equal(t, VerdictPass, r.Verdict)
	case "advance":
		assert.Equal(t, 2, after.CurrentRound)
	case "close":
		assert.Equal(t, StatusClosed, after.Status)
	case "abandon":
		assert.Equal(t, StatusAbandoned, after.Status)
	}
}

func retainAcceptedReflection(t *testing.T, s *Store, c *Contract) *reflectionIntent {
	t.Helper()
	s.persistenceCheck = func(phase string) error {
		if phase == "reflect_archive" {
			return errors.New("interrupted optional archive")
		}
		return nil
	}
	out, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
	require.NoError(t, err)
	require.Equal(t, "applied", out.Application)
	require.Equal(t, "pending", out.Trace)
	s.persistenceCheck = nil
	in, err := s.readRoundIntent(c.MissionID, 1)
	require.NoError(t, err)
	require.NotNil(t, in)
	return in
}

func TestMutationOutcomeOptionalRecovery(t *testing.T) {
	for _, operation := range ordinaryMutations {
		for _, fault := range []string{"conflict", "permission", "cleanup-sync"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				s, c := prepareOrdinaryMutation(t, operation)
				in := retainAcceptedReflection(t, s, c)
				receipt, err := os.ReadFile(s.intentPath(c.MissionID, 1))
				require.NoError(t, err)
				want := "optional reflection detail conflict"
				switch fault {
				case "conflict":
					other := *in
					other.Reflection.Reason = "divergent optional prose"
					data, err := yaml.Marshal(other)
					require.NoError(t, err)
					path := s.reflectionArchivePath(c.MissionID, 1)
					require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
					require.NoError(t, os.WriteFile(path, data, 0o600))
				case "permission":
					want = "permission denied"
					s.persistenceCheck = func(phase string) error {
						if phase == "reflect_archive" {
							return os.ErrPermission
						}
						return nil
					}
				case "cleanup-sync":
					want = "cleanup directory sync failed"
					real := syncDir
					t.Cleanup(func() { syncDir = real })
					syncDir = func(dir string) error {
						if dir == filepath.Dir(s.intentPath(c.MissionID, 1)) {
							return errors.New(want)
						}
						return real(dir)
					}
				}
				var out MutationOutcome
				stderr := captureStderr(t, func() {
					out, err = ordinaryMutation(s, c, operation)
				})
				require.NoError(t, err)
				assert.Empty(t, stderr, "outcome API must not print the same warning")
				require.Len(t, out.Warnings, 1)
				assert.Contains(t, out.Warnings[0], "accepted; optional reflection trace pending")
				assert.Contains(t, out.Warnings[0], want)
				assertOrdinaryMutation(t, s, c, operation)
				if fault != "cleanup-sync" {
					after, err := os.ReadFile(s.intentPath(c.MissionID, 1))
					require.NoError(t, err)
					assert.Equal(t, receipt, after, "failed optional work retains the receipt")
				} else {
					assert.FileExists(t, s.reflectionArchivePath(c.MissionID, 1), "cleanup failure must not erase retained detail")
				}
			})
		}
	}
}

func TestMutationOutcomeInstalledDurability(t *testing.T) {
	for _, operation := range ordinaryMutations {
		t.Run(operation, func(t *testing.T) {
			s, c := prepareOrdinaryMutation(t, operation)
			if operation == "advance" {
				require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue)))
			}
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			path, err := s.ContractPath(c.MissionID)
			require.NoError(t, err)
			failed := false
			syncDir = func(dir string) error {
				if dir == filepath.Dir(path) {
					failed = true
					return errors.New("installed destination sync failure")
				}
				return real(dir)
			}
			var out MutationOutcome
			stderr := captureStderr(t, func() { out, err = ordinaryMutation(s, c, operation) })
			require.NoError(t, err)
			require.True(t, failed)
			assert.Empty(t, stderr)
			require.Len(t, out.Warnings, 1)
			assert.Contains(t, out.Warnings[0], "installed; crash durability unconfirmed")
			assert.Contains(t, out.Warnings[0], "installed destination sync failure")
			assertOrdinaryMutation(t, s, c, operation)
		})
	}
}

func TestMutationOutcomeRequiredFailureRollsBack(t *testing.T) {
	for _, operation := range ordinaryMutations {
		for _, rollbackFault := range []bool{false, true} {
			name := operation + "/restored"
			if rollbackFault {
				name = operation + "/rollback-durability-failed"
			}
			t.Run(name, func(t *testing.T) {
				s, c := prepareOrdinaryMutation(t, operation)
				if operation == "advance" || operation == "result" {
					// Keep an existing results file for the result rollback path.
					if operation == "result" {
						require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
					}
					require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue)))
					if operation == "result" {
						_, err := s.AdvanceRound(c.MissionID, c.Leader)
						require.NoError(t, err)
						c, _ = s.Load(c.MissionID)
						require.NotNil(t, c)
					}
				}
				path, err := s.ContractPath(c.MissionID)
				require.NoError(t, err)
				if operation == "result" {
					path, err = s.resultsPath(c.MissionID)
					require.NoError(t, err)
				}
				before, err := os.ReadFile(path)
				require.NoError(t, err)
				live := (audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "original"}).LivePath(s.policy)
				events, err := os.ReadFile(live)
				require.NoError(t, err)
				real := syncDir
				t.Cleanup(func() { syncDir = real })
				required := errors.New("required event sync failed")
				failedEvent := false
				syncDir = func(dir string) error {
					if dir == filepath.Dir(live) {
						failedEvent = true
						return required
					}
					if rollbackFault && failedEvent && dir == filepath.Dir(path) {
						return errors.New("rollback directory sync failed")
					}
					if !failedEvent && dir == filepath.Dir(path) {
						return errors.New("forward install durability unconfirmed")
					}
					return real(dir)
				}
				var out MutationOutcome
				stderr := captureStderr(t, func() { out, err = ordinaryMutation(s, c, operation) })
				require.ErrorIs(t, err, required)
				if rollbackFault {
					assert.Contains(t, err.Error(), "rollback failed")
					assert.Contains(t, err.Error(), "rollback directory sync failed")
				} else {
					assert.Contains(t, err.Error(), "rolled back")
				}
				assert.Empty(t, out.Warnings, "transaction failures must not become success diagnostics")
				assert.Empty(t, stderr)
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				after, err = os.ReadFile(live)
				require.NoError(t, err)
				assert.Equal(t, events, after)
			})
		}
	}
}

func TestMutationOutcomeRecoveryWarningsRequiredFailure(t *testing.T) {
	for _, operation := range ordinaryMutations {
		t.Run(operation, func(t *testing.T) {
			s, c := prepareOrdinaryMutation(t, operation)
			retainAcceptedReflection(t, s, c)
			recovered := false
			s.persistenceCheck = func(phase string) error {
				if phase == "reflect_archive" {
					recovered = true
					return errors.New("prior accepted optional archive unavailable")
				}
				return nil
			}
			path, err := s.ContractPath(c.MissionID)
			require.NoError(t, err)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			live := (audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "original"}).LivePath(s.policy)
			eventsBefore, err := os.ReadFile(live)
			require.NoError(t, err)
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			required := errors.New("current required event sync failed")
			failedEvent, failedForward := false, false
			syncDir = func(dir string) error {
				// Let recovery confirm its already accepted authority first.
				if recovered && dir == filepath.Dir(live) {
					failedEvent = true
					return required
				}
				if recovered && !failedEvent && dir == filepath.Dir(path) {
					failedForward = true
					return errors.New("current forward install durability unconfirmed")
				}
				return real(dir)
			}
			var out MutationOutcome
			stderr := captureStderr(t, func() { out, err = ordinaryMutation(s, c, operation) })
			require.ErrorIs(t, err, required)
			assert.Contains(t, err.Error(), "rolled back")
			assert.True(t, failedForward)
			assert.True(t, failedEvent)
			assert.Empty(t, stderr)
			require.Len(t, out.Warnings, 1, "retain prior recovery only, not failed forward-write diagnostics")
			assert.Contains(t, out.Warnings[0], "accepted; optional reflection trace pending")
			assert.Contains(t, out.Warnings[0], "prior accepted optional archive unavailable")
			assert.NotContains(t, out.Warnings[0], "current forward install")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			eventsAfter, err := os.ReadFile(live)
			require.NoError(t, err)
			assert.Equal(t, eventsBefore, eventsAfter)
			if operation == "result" {
				r, err := s.LoadResult(c.MissionID, 1)
				require.NoError(t, err)
				assert.Nil(t, r)
			}
		})
	}
}

func TestMutationOutcomeFirstResultRollback(t *testing.T) {
	for _, rollbackFault := range []bool{false, true} {
		t.Run(fmt.Sprint(rollbackFault), func(t *testing.T) {
			s, c := authorityStore(t, 3)
			path, err := s.resultsPath(c.MissionID)
			require.NoError(t, err)
			live := (audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "original"}).LivePath(s.policy)
			required := errors.New("required event sync failed")
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			failedEvent := false
			syncDir = func(dir string) error {
				if dir == filepath.Dir(live) {
					failedEvent = true
					return required
				}
				if rollbackFault && failedEvent && dir == filepath.Dir(path) {
					return errors.New("rollback removal sync failed")
				}
				return real(dir)
			}
			out, err := s.AppendResultWithOutcome(c.MissionID, resultFor(c, VerdictPass))
			require.ErrorIs(t, err, required)
			if rollbackFault {
				assert.Contains(t, err.Error(), "rollback failed")
				assert.Contains(t, err.Error(), "rollback removal sync failed")
			} else {
				assert.Contains(t, err.Error(), "rolled back")
			}
			assert.Empty(t, out.Warnings)
			assert.NoFileExists(t, path)
		})
	}
}

func TestMutationOutcomeCompatibilityWarningsOnce(t *testing.T) {
	for _, operation := range ordinaryMutations {
		t.Run(operation, func(t *testing.T) {
			s, c := prepareOrdinaryMutation(t, operation)
			retainAcceptedReflection(t, s, c)
			s.persistenceCheck = func(phase string) error {
				if phase == "reflect_archive" {
					return os.ErrPermission
				}
				return nil
			}
			stderr := captureStderr(t, func() {
				var err error
				switch operation {
				case "update":
					loaded, loadErr := s.Load(c.MissionID)
					require.NoError(t, loadErr)
					err = s.Update(loaded)
				case "result":
					err = s.AppendResult(c.MissionID, resultFor(c, VerdictPass))
				case "advance":
					_, err = s.AdvanceRound(c.MissionID, c.Leader)
				case "close":
					_, err = s.Close(c.MissionID, StatusClosed)
				case "abandon":
					_, err = s.Abandon(c.MissionID, "cancelled")
				}
				require.NoError(t, err)
			})
			assert.Equal(t, 1, strings.Count(stderr, "accepted; optional reflection trace pending"))
			assert.Contains(t, stderr, "ethos: mission "+c.MissionID)
		})
	}
}

func TestMutationOutcomePreinstallFailure(t *testing.T) {
	for _, operation := range ordinaryMutations {
		t.Run(operation, func(t *testing.T) {
			s, c := prepareOrdinaryMutation(t, operation)
			if operation == "advance" {
				require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue)))
			}
			path, err := s.ContractPath(c.MissionID)
			require.NoError(t, err)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			// Fail local staging before a destination can be installed. Unlike a
			// chmod fixture this fails deterministically even under a root runner.
			require.NoError(t, os.Remove(s.policy.TempDir()))
			require.NoError(t, os.WriteFile(s.policy.TempDir(), []byte("blocked staging"), 0o600))
			out, err := ordinaryMutation(s, c, operation)
			require.Error(t, err)
			assert.Empty(t, out.Warnings)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			if operation == "result" {
				r, err := s.LoadResult(c.MissionID, 1)
				require.NoError(t, err)
				assert.Nil(t, r)
			}
		})
	}
}

func TestMutationOutcomeMalformedInput(t *testing.T) {
	s, c := authorityStore(t, 3)
	out, err := s.UpdateWithOutcome(nil)
	require.ErrorContains(t, err, "contract is nil")
	assert.Empty(t, out.Warnings)
	out, err = s.AppendResultWithOutcome(c.MissionID, nil)
	require.ErrorContains(t, err, "result is nil")
	assert.Empty(t, out.Warnings)
	_, out, err = s.CloseWithOutcome(c.MissionID, "invalid-status")
	require.ErrorContains(t, err, "invalid close status")
	assert.Empty(t, out.Warnings)
	_, out, err = s.AbandonWithOutcome(c.MissionID, " ")
	require.Error(t, err)
	assert.Empty(t, out.Warnings)
	_, out, err = s.AdvanceRoundWithOutcome("../invalid", c.Leader)
	require.Error(t, err)
	assert.Empty(t, out.Warnings)
}
