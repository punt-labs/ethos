package mission

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func authorityStore(t *testing.T, rounds int) (*Store, *Contract) {
	t.Helper()
	p, err := layout.NewRepository(t.TempDir(), t.TempDir(), layout.RecordBlame)
	require.NoError(t, err)
	s := NewRepositoryStore(p, t.TempDir()).WithSessionID("original")
	c := newContract("m-2026-11-04-910")
	c.Budget.Rounds = rounds
	require.NoError(t, s.Create(c))
	return s, c
}

func TestReflectionAuthorityInterruptionRetry(t *testing.T) {
	for _, phase := range []string{"event_prepared", "reflect_event_delivered", "reflect_decision_installed", "reflect_archive"} {
		t.Run(phase, func(t *testing.T) {
			s, c := authorityStore(t, 3)
			s.persistenceCheck = func(p string) error {
				if p == phase {
					return errors.New("interrupted at " + p)
				}
				return nil
			}
			r := reflectionFor(c.MissionID, 1, RecommendationContinue)
			out, err := s.ApplyReflection(c.MissionID, r)
			accepted := phase == "reflect_decision_installed" || phase == "reflect_archive"
			if accepted {
				require.NoError(t, err)
				assert.Equal(t, "applied", out.Application)
			} else {
				require.Error(t, err)
				assert.Equal(t, "not_applied", out.Application)
			}
			assert.Equal(t, "pending", out.Trace)
			in, err := s.readRoundIntent(c.MissionID, 1)
			require.NoError(t, err)
			require.NotNil(t, in)
			states, err := s.ReadReflections(c.MissionID)
			require.NoError(t, err)
			assert.True(t, states[0].Pending)
			if !accepted {
				assert.Equal(t, "absent", states[0].DecisionState)
				_, err := s.AdvanceRound(c.MissionID, "claude")
				require.ErrorContains(t, err, "pending reflection")
			}
			// A fresh process/session must reuse the original delivery identity.
			retry := NewRepositoryStore(s.policy, t.TempDir()).WithSessionID("retry")
			r2 := reflectionFor(c.MissionID, 1, RecommendationContinue)
			out, err = retry.ApplyReflection(c.MissionID, r2)
			require.NoError(t, err)
			assert.Equal(t, "confirmed", out.Durability)
			assert.Equal(t, in.Reflection.CreatedAt, r2.CreatedAt)
			archive, err := readIntent(s.reflectionArchivePath(c.MissionID, 1), c.MissionID)
			require.NoError(t, err)
			require.NotNil(t, archive)
			assert.Equal(t, *in, *archive)
			events, warnings, err := retry.LoadEvents(c.MissionID)
			require.NoError(t, err)
			assert.Empty(t, warnings)
			count := 0
			for _, event := range events {
				if event.OperationID == in.Decision.ID {
					count++
					assert.Equal(t, in.Event.TS, event.TS)
					assert.Equal(t, "accepted", event.Acceptance)
				}
			}
			assert.Equal(t, 1, count)
			assert.NoFileExists(t, (audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "retry"}).LivePath(s.policy))
		})
	}
}

func TestReflectionAuthorityAllRecommendationsAndBudget(t *testing.T) {
	for _, rec := range []string{RecommendationContinue, RecommendationAdvance, RecommendationPivot, RecommendationStop, RecommendationEscalate} {
		t.Run(rec, func(t *testing.T) {
			s, c := authorityStore(t, 10)
			r := reflectionFor(c.MissionID, 1, rec)
			require.NoError(t, s.AppendReflection(c.MissionID, r))
			// Delete local prose to model an isolated clone. The compact
			// authority must still enforce the same recommendation.
			require.NoError(t, os.Remove(s.reflectionArchivePath(c.MissionID, 1)))
			states, err := s.ReadReflections(c.MissionID)
			require.NoError(t, err)
			assert.Equal(t, "unavailable_here", states[0].DetailState)
			n, err := s.AdvanceRound(c.MissionID, "claude")
			if IsTerminalRecommendation(rec) {
				require.ErrorContains(t, err, rec)
				assert.Contains(t, err.Error(), "unavailable here")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 2, n)
			for round := 2; round <= 10; round++ {
				require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, round, RecommendationContinue)))
				n, err = s.AdvanceRound(c.MissionID, "claude")
				if round == 10 {
					require.ErrorContains(t, err, "exhausted")
				} else {
					require.NoError(t, err)
					assert.Equal(t, round+1, n)
				}
			}
		})
	}
}

func TestReflectionAuthorityConcurrentDuplicate(t *testing.T) {
	s, c := authorityStore(t, 3)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other := NewRepositoryStore(s.policy, t.TempDir())
			_, err := other.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Len(t, loaded.RoundDecisions.Entries, 1)
}

func TestRoundDecisionAuthorityMalformed(t *testing.T) {
	s, c := authorityStore(t, 3)
	path := mustContractPath(t, s, c.MissionID)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, yaml.Unmarshal(b, &fields))
	delete(fields, "round_decisions")
	base, err := yaml.Marshal(fields)
	require.NoError(t, err)
	for _, block := range []string{
		"null", "{version: 1}", "{entries: []}", "{version: 2, entries: []}", "{version: 1, entries: null}",
		"{version: 1, version: 1, entries: []}", "{version: 1, entries: [], extra: true}",
		"{version: 1, entries: &x [*x]}", "{version: 1, entries: []}\n---\nother: true",
		"{version: 1, entries: [{id: 0123456789abcdef0123456789abcdef, round: 1, author: claude, recommendation: stop}]}",
		"{version: 1, entries: [{id: 0123456789abcdef0123456789abcdef, round: 1, created_at: '', author: claude, recommendation: continue}]}",
		"{version: 1, entries: [{id: 0123456789abcdef0123456789abcdef, round: 1, created_at: invalid, author: claude, recommendation: continue}]}",
	} {
		t.Run(strings.ReplaceAll(block, "/", "-"), func(t *testing.T) {
			require.NoError(t, os.WriteFile(path, append(append([]byte{}, base...), []byte("round_decisions: "+block+"\n")...), 0o600))
			_, err := s.Load(c.MissionID)
			require.Error(t, err)
			_, err = s.AdvanceRound(c.MissionID, "claude")
			require.Error(t, err, "unreadable authority must not grant advancement")
		})
	}
}

func TestReflectionAuthorityUpdateFrozenFields(t *testing.T) {
	s, c := authorityStore(t, 3)
	for _, mutate := range []func(*Contract){
		func(c *Contract) { c.RoundDecisions = nil },
		func(c *Contract) { c.CurrentRound++ },
		func(c *Contract) { c.Status = StatusClosed },
		func(c *Contract) { c.ClosedAt = "2026-11-04T00:00:00Z" },
		func(c *Contract) { c.Budget.Rounds++ },
		func(c *Contract) { c.Evaluator.PinnedAt = "2026-11-04T00:00:00Z" },
	} {
		loaded, err := s.Load(c.MissionID)
		require.NoError(t, err)
		mutate(loaded)
		require.ErrorContains(t, s.Update(loaded), "lifecycle authority")
	}
	stale, err := s.Load(c.MissionID)
	require.NoError(t, err)
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue)))
	require.ErrorContains(t, s.Update(stale), "lifecycle authority")
	// No staging, locks, prose, or events are created inside the record tree.
	require.NoError(t, filepath.WalkDir(s.policy.RecordRoot(), func(path string, e os.DirEntry, err error) error {
		require.NoError(t, err)
		assert.NotContains(t, e.Name(), "ethos-tmp")
		assert.False(t, strings.HasSuffix(e.Name(), ".lock"), path)
		assert.False(t, strings.HasSuffix(e.Name(), ".jsonl"), path)
		return nil
	}))
}

func TestReflectionAuthorityOptionalTraceFailureDoesNotFenceAdvance(t *testing.T) {
	for _, phase := range []string{"reflect_archive", "reflect_publish"} {
		t.Run(phase, func(t *testing.T) {
			s, c := authorityStore(t, 3)
			if phase == "reflect_publish" {
				p, err := layout.NewRepository(s.repoRoot, s.checkoutRoot, layout.RecordFull)
				require.NoError(t, err)
				s = NewRepositoryStore(p, s.root).WithSessionID("original")
			}
			s.persistenceCheck = func(p string) error {
				if p == phase {
					return errors.New("persistent optional trace fault")
				}
				return nil
			}
			r := reflectionFor(c.MissionID, 1, RecommendationContinue)
			out, err := s.ApplyReflection(c.MissionID, r)
			require.NoError(t, err)
			assert.Equal(t, "applied", out.Application)
			assert.Equal(t, "confirmed", out.Durability)
			assert.Equal(t, "pending", out.Trace)
			first, err := s.readRoundIntent(c.MissionID, 1)
			require.NoError(t, err)
			require.NotNil(t, first)
			n, err := s.AdvanceRound(c.MissionID, "claude")
			require.NoError(t, err, "optional trace failure must not weaken shared decision authority")
			assert.Equal(t, 2, n)
			out, err = s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 2, RecommendationContinue))
			require.NoError(t, err)
			assert.Equal(t, "applied", out.Application)
			retained, err := s.readRoundIntent(c.MissionID, 1)
			require.NoError(t, err)
			require.Equal(t, first, retained, "next round must not overwrite the only durable full payload")
			states, err := s.ReadReflections(c.MissionID)
			require.NoError(t, err)
			require.Len(t, states, 2)
			assert.Equal(t, "present", states[0].DetailState)
			assert.Equal(t, *r, *states[0].Reflection)
			// A fresh store can finish an earlier accepted round after a later
			// round has been prepared, without another required event append.
			retry := NewRepositoryStore(s.policy, s.root).WithSessionID("retry")
			out, err = retry.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
			require.NoError(t, err)
			assert.Equal(t, "already_applied", out.Application)
			assert.Equal(t, "complete", out.Trace)
			archive, err := readIntent(s.reflectionArchivePath(c.MissionID, 1), c.MissionID)
			require.NoError(t, err)
			require.Equal(t, first, archive)
			events, warnings, err := retry.LoadEvents(c.MissionID)
			require.NoError(t, err)
			assert.Empty(t, warnings)
			counts := map[string]int{}
			advances := 0
			for _, event := range events {
				if event.OperationID != "" {
					counts[event.OperationID]++
				}
				if event.Event == "round_advanced" {
					advances++
				}
			}
			assert.Equal(t, 1, counts[first.Decision.ID])
			assert.Equal(t, 1, advances)
		})
	}
}

func TestReflectionAuthorityOptionalConflictDoesNotFenceLifecycle(t *testing.T) {
	for _, source := range []string{"archive", "published"} {
		for _, action := range []string{"advance", "close"} {
			t.Run(source+"/"+action, func(t *testing.T) {
				s, c := authorityStore(t, 3)
				if source == "published" {
					p, err := layout.NewRepository(s.repoRoot, s.checkoutRoot, layout.RecordFull)
					require.NoError(t, err)
					s = NewRepositoryStore(p, s.root).WithSessionID("original")
				}
				if action == "close" {
					require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
				}
				s.persistenceCheck = func(phase string) error {
					if phase == "reflect_archive" {
						return errors.New("interrupted optional archival")
					}
					return nil
				}
				r := reflectionFor(c.MissionID, 1, RecommendationContinue)
				out, err := s.ApplyReflection(c.MissionID, r)
				require.NoError(t, err)
				assert.Equal(t, "applied", out.Application)
				assert.Equal(t, "pending", out.Trace)
				s.persistenceCheck = nil
				in, err := s.readRoundIntent(c.MissionID, 1)
				require.NoError(t, err)
				require.NotNil(t, in)
				receiptBefore, err := os.ReadFile(s.intentPath(c.MissionID, 1))
				require.NoError(t, err)
				other := *in
				other.Reflection.Reason = "different optional prose with the same compact decision and event"
				require.NoError(t, other.validate(c.MissionID))
				assert.Equal(t, in.Decision, other.Decision)
				assert.Equal(t, in.Event, other.Event)
				path := s.reflectionArchivePath(c.MissionID, 1)
				var data []byte
				if source == "archive" {
					data, err = yaml.Marshal(other)
				} else {
					path = filepath.Join(s.policy.CheckoutRecordRoot(), "missions", c.MissionID, "reflections.yaml")
					data, err = yaml.Marshal(reflectionsFile{Reflections: []Reflection{other.Reflection}})
				}
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, data, 0o600))
				// The exact retry reports optional divergence, not failed acceptance.
				out, err = s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
				require.NoError(t, err)
				assert.Equal(t, "already_applied", out.Application)
				assert.Equal(t, "confirmed", out.Durability)
				assert.Equal(t, "pending", out.Trace)
				assert.Contains(t, strings.Join(out.Diagnostics, "\n"), "optional reflection detail conflict")
				warning := captureStderr(t, func() {
					if action == "advance" {
						n, err := s.AdvanceRound(c.MissionID, "claude")
						require.NoError(t, err)
						assert.Equal(t, 2, n)
					} else {
						result, err := s.Close(c.MissionID, StatusClosed)
						require.NoError(t, err)
						assert.Equal(t, 1, result.Round)
					}
				})
				assert.Contains(t, warning, "accepted; optional reflection trace pending")
				assert.Contains(t, warning, "optional reflection detail conflict")
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, data, after, "leave divergent optional bytes untouched")
				receiptAfter, err := os.ReadFile(s.intentPath(c.MissionID, 1))
				require.NoError(t, err)
				assert.Equal(t, receiptBefore, receiptAfter, "retain the original complete accepted payload")
			})
		}
	}
}

func TestReflectionAuthorityUnreadableApplicationUnknown(t *testing.T) {
	for _, source := range []string{"direct", "two-reflection-receipts", "correction-receipt"} {
		for _, fault := range []string{"malformed", "directory", "missing"} {
			t.Run(source+"/"+fault, func(t *testing.T) {
				s, c := authorityStore(t, 3)
				s.persistenceCheck = func(phase string) error {
					if phase == "reflect_archive" {
						return errors.New("retain accepted receipt")
					}
					return nil
				}
				if source == "two-reflection-receipts" {
					_, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
					require.NoError(t, err)
					_, err = s.AdvanceRound(c.MissionID, "claude")
					require.NoError(t, err)
					_, err = s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 2, RecommendationContinue))
					require.NoError(t, err)
				} else if source == "correction-receipt" {
					require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
					_, err := s.Close(c.MissionID, StatusClosed)
					require.NoError(t, err)
					s.persistenceCheck = func(phase string) error {
						if phase == "event_prepared" {
							return errors.New("retain prepared correction")
						}
						return nil
					}
					require.Error(t, s.Correct(c.MissionID, Correction{Mission: c.MissionID, Author: "claude", Kind: CorrectionDecision, Corrected: "new decision"}))
				}
				s.persistenceCheck = nil
				paths := []string{(audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "original"}).LivePath(s.policy)}
				if source == "two-reflection-receipts" {
					paths = append(paths, s.intentPath(c.MissionID, 1), s.intentPath(c.MissionID, 2))
				} else if source == "correction-receipt" {
					paths = append(paths, s.correctionIntentPath(c.MissionID))
				}
				before := make(map[string][]byte)
				for _, path := range paths {
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					before[path] = data
				}
				path := mustContractPath(t, s, c.MissionID)
				switch fault {
				case "malformed":
					require.NoError(t, os.WriteFile(path, []byte("round_decisions: null\n"), 0o600))
				case "directory":
					require.NoError(t, os.Remove(path))
					require.NoError(t, os.Mkdir(path, 0o700))
				case "missing":
					require.NoError(t, os.Remove(path))
				}
				for _, round := range []int{1, 2} {
					out, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, round, RecommendationContinue))
					require.ErrorIs(t, err, errAuthorityRead)
					assert.Equal(t, "unknown", out.Application)
				}
				for _, path := range paths {
					after, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, before[path], after, "authority uncertainty must not change receipts or events")
				}
			})
		}
	}
}

func TestReflectionAuthorityInterruptedRetryPreservesConsent(t *testing.T) {
	for _, originalMode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		originalRecord := "blame"
		if originalMode == layout.RecordFull {
			originalRecord = "full"
		}
		t.Run(originalRecord, func(t *testing.T) {
			s, c := authorityStore(t, 3)
			originalPolicy, err := layout.NewRepository(s.repoRoot, s.checkoutRoot, originalMode)
			require.NoError(t, err)
			s = NewRepositoryStore(originalPolicy, s.root).WithSessionID("original")
			s.persistenceCheck = func(phase string) error {
				if phase == "event_prepared" {
					return errors.New("interrupted before required append")
				}
				return nil
			}
			_, err = s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
			require.Error(t, err)
			in, err := s.readRoundIntent(c.MissionID, 1)
			require.NoError(t, err)
			require.NotNil(t, in)
			nextMode := layout.RecordFull
			if originalMode == layout.RecordFull {
				nextMode = layout.RecordBlame
			}
			nextPolicy, err := layout.NewRepository(s.repoRoot, s.checkoutRoot, nextMode)
			require.NoError(t, err)
			retry := NewRepositoryStore(nextPolicy, s.root).WithSessionID("retry")
			out, err := retry.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
			require.NoError(t, err)
			assert.Equal(t, "applied", out.Application)
			assert.Equal(t, "complete", out.Trace)
			archive, err := readIntent(s.reflectionArchivePath(c.MissionID, 1), c.MissionID)
			require.NoError(t, err)
			require.Equal(t, in, archive, "keep the original payload, session, time and consent")
			assert.Equal(t, originalRecord, archive.Event.Record)
			published := filepath.Join(nextPolicy.CheckoutRecordRoot(), "missions", c.MissionID, "reflections.yaml")
			if originalMode == layout.RecordFull {
				assert.FileExists(t, published, "eligible pending publication survives switching to blame")
			} else {
				assert.NoFileExists(t, published, "a full-mode retry must not promote private preparation")
			}
			assert.NoFileExists(t, (audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "retry"}).LivePath(nextPolicy))
		})
	}
}

func TestReflectionAuthorityRequiredConflictStillFencesLifecycle(t *testing.T) {
	for _, conflict := range []string{"authority", "event"} {
		t.Run(conflict, func(t *testing.T) {
			s, c := authorityStore(t, 3)
			require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
			s.persistenceCheck = func(phase string) error {
				if phase == "reflect_archive" {
					return errors.New("retain receipt")
				}
				return nil
			}
			_, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
			require.NoError(t, err)
			s.persistenceCheck = nil
			if conflict == "authority" {
				loaded, err := s.Load(c.MissionID)
				require.NoError(t, err)
				loaded.RoundDecisions.Entries[0].Author = "other"
				body, err := yaml.Marshal(loaded)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(mustContractPath(t, s, c.MissionID), body, 0o600))
			} else {
				path := (audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "original"}).LivePath(s.policy)
				body, err := os.ReadFile(path)
				require.NoError(t, err)
				var changed []byte
				for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
					var event Event
					require.NoError(t, json.Unmarshal([]byte(line), &event))
					if event.Event == "reflect" {
						event.Details["signal_count"] = 999
					}
					encoded, err := json.Marshal(event)
					require.NoError(t, err)
					changed = append(changed, append(encoded, '\n')...)
				}
				require.NoError(t, os.WriteFile(path, changed, 0o600))
			}
			_, err = s.AdvanceRound(c.MissionID, "claude")
			require.ErrorContains(t, err, "conflicts")
			_, err = s.Close(c.MissionID, StatusClosed)
			require.ErrorContains(t, err, "conflicts")
			loaded, err := s.Load(c.MissionID)
			require.NoError(t, err)
			assert.Equal(t, StatusOpen, loaded.Status)
			assert.Equal(t, 1, loaded.CurrentRound)
			assert.FileExists(t, s.intentPath(c.MissionID, 1))
		})
	}
}

func TestReflectionAuthorityReconcileWarnings(t *testing.T) {
	s, c := authorityStore(t, 3)
	s.persistenceCheck = func(phase string) error {
		if phase == "reflect_archive" {
			return errors.New("optional archive interrupted")
		}
		return nil
	}
	out, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
	require.NoError(t, err)
	require.Equal(t, "applied", out.Application)
	require.Equal(t, "pending", out.Trace)
	receiptPath := s.intentPath(c.MissionID, 1)
	receiptBefore, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	contractPath := mustContractPath(t, s, c.MissionID)
	contractBefore, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	var warnings []string
	warn := func(message string) { warnings = append(warnings, message) }
	message := "mission " + c.MissionID + " round 1 accepted; optional reflection trace pending: optional archive interrupted"
	stderr := captureStderr(t, func() {
		require.NoError(t, s.withMissionLock(c.MissionID, true, func() error {
			// A real round skips only its own receipt; 0 skips none.
			require.NoError(t, s.reconcileReflectionsExceptWithWarnings(c.MissionID, 1, warn))
			require.Empty(t, warnings)
			return s.reconcileReflectionsExceptWithWarnings(c.MissionID, 0, warn)
		}))
	})
	assert.Empty(t, stderr, "a supplied callback replaces the stderr diagnostic")
	assert.Equal(t, []string{message}, warnings)
	stderr = captureStderr(t, func() {
		require.NoError(t, s.withMissionLock(c.MissionID, true, func() error {
			return s.reconcileReflection(c.MissionID)
		}))
	})
	assert.Equal(t, "ethos: "+message+"\n", stderr, "the existing wrapper retains exact stderr behavior")

	// Required-event corruption remains an error, never an optional warning.
	in, err := s.readRoundIntent(c.MissionID, 1)
	require.NoError(t, err)
	require.NotNil(t, in)
	conflict := in.Event
	ts, err := audit.ParseLineTS(conflict.TS)
	require.NoError(t, err)
	conflict.TS = audit.FormatLineTS(ts + 1)
	f := requiredReplayFixture{store: s, id: c.MissionID, event: in.Event, session: in.EventSession}
	replaceReplayLive(t, f, in.Event, conflict)
	warnings = nil
	stderr = captureStderr(t, func() {
		err = s.withMissionLock(c.MissionID, true, func() error {
			return s.reconcileReflectionsExceptWithWarnings(c.MissionID, 0, warn)
		})
	})
	assert.ErrorContains(t, err, "divergent")
	assert.Empty(t, warnings)
	assert.Empty(t, stderr)
	receiptAfter, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	assert.Equal(t, receiptBefore, receiptAfter)
	contractAfter, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	assert.Equal(t, contractBefore, contractAfter)
}

func TestReflectionAuthorityAcceptedReceiptCanonicalPayload(t *testing.T) {
	for _, action := range []string{"reconcile", "retry"} {
		for _, layer := range []string{"live", "archive"} {
			for _, variant := range replayPayloadVariants() {
				t.Run(action+"/"+layer+"/"+variant.name, func(t *testing.T) {
					s, c := authorityStore(t, 3)
					s.persistenceCheck = func(phase string) error {
						if phase == "reflect_decision_installed" {
							return errors.New("retain accepted receipt before optional cleanup")
						}
						return nil
					}
					out, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
					require.NoError(t, err)
					require.Equal(t, "applied", out.Application)
					require.Equal(t, "unconfirmed", out.Durability)
					s.persistenceCheck = nil
					in, err := s.readRoundIntent(c.MissionID, 1)
					require.NoError(t, err)
					require.NotNil(t, in)
					f := requiredReplayFixture{store: s, id: c.MissionID, event: in.Event, session: in.EventSession, receipt: s.intentPath(c.MissionID, 1)}
					candidate := installSoleReplayPayload(t, f, layer, replayPayloadBytes(t, in.Event, variant))
					paths := []string{mustContractPath(t, s, c.MissionID), s.correctionsPath(c.MissionID), f.receipt, candidate,
						(audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(s.policy)}
					before := make(map[string][]byte)
					for _, path := range paths {
						data, err := os.ReadFile(path)
						require.NoError(t, err)
						before[path] = data
					}
					retry := NewRepositoryStore(s.policy, s.root).WithSessionID("retry")
					if action == "reconcile" {
						err = retry.withMissionLock(c.MissionID, true, func() error { return retry.reconcileReflection(c.MissionID) })
						if variant.before != "" {
							assert.ErrorContains(t, err, "conflicts with receipt")
						} else {
							require.NoError(t, err)
						}
					} else {
						out, err = retry.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
						require.NoError(t, err, "accepted authority is not rolled back on a required-event conflict")
						assert.Equal(t, "already_applied", out.Application)
						if variant.before != "" {
							assert.Equal(t, "unconfirmed", out.Durability)
							assert.Equal(t, "pending", out.Trace)
							assert.Contains(t, strings.Join(out.Diagnostics, "\n"), "conflicts with receipt")
						} else {
							assert.Equal(t, "confirmed", out.Durability)
							assert.Equal(t, "complete", out.Trace)
						}
					}
					if variant.before == "" {
						delete(before, f.receipt)
						assert.NoFileExists(t, f.receipt)
						assert.FileExists(t, s.reflectionArchivePath(c.MissionID, 1))
					} else {
						assert.NoFileExists(t, s.reflectionArchivePath(c.MissionID, 1), "conflict must not materialize accepted detail")
					}
					for path, want := range before {
						got, err := os.ReadFile(path)
						if assert.NoError(t, err, "retain %s", path) {
							assert.Equal(t, want, got, "reconciliation must not alter %s", path)
						}
					}
				})
			}
		}
	}
}

func TestPreparedEventCanonicalValidation(t *testing.T) {
	prepared := Event{TS: "2026-11-05T00:00:00Z", Event: "reflect", Actor: "claude", Record: "blame",
		OperationID: strings.Repeat("a", 32), Details: map[string]any{"round": 1}}
	raw, err := json.Marshal(prepared)
	require.NoError(t, err)
	body := string(raw)
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{"unknown-field", strings.TrimSuffix(body, "}") + `,"unexpected":true}`},
		{"duplicate-field", strings.Replace(body, `"actor":"claude"`, `"actor":"claude","actor":"claude"`, 1)},
		{"trailing-document", body + ` {}`},
		{"invalid-timestamp", strings.Replace(body, prepared.TS, "not-a-timestamp", 1)},
		{"empty-actor", strings.Replace(body, `"actor":"claude"`, `"actor":""`, 1)},
		{"invalid-record", strings.Replace(body, `"record":"blame"`, `"record":"invalid"`, 1)},
		{"invalid-operation-id", strings.Replace(body, prepared.OperationID, "invalid", 1)},
		{"invalid-number", strings.Replace(body, `"round":1`, `"round":01`, 1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			matching, err := matchesPreparedEvent([]byte(tt.raw), prepared)
			assert.Error(t, err)
			assert.False(t, matching)
		})
	}
}
