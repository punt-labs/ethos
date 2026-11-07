package mission

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise an installed authority left by a real post-rename sync failure, not
// an unaccepted preparation. Recovery may verify it, never recreate its event.
func acceptedCorrectionRecovery(t *testing.T) requiredReplayFixture {
	t.Helper()
	s, c := authorityStore(t, 3)
	require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
	_, err := s.Close(c.MissionID, StatusClosed)
	require.NoError(t, err)
	correction := Correction{Mission: c.MissionID, Round: 1, Author: "claude", Kind: CorrectionDecision, Corrected: "accepted annotation"}
	real := syncDir
	syncDir = func(dir string) error {
		if dir == filepath.Dir(s.correctionsPath(c.MissionID)) {
			return errors.New("installed correction sync interrupted")
		}
		return real(dir)
	}
	err = s.Correct(c.MissionID, correction)
	syncDir = real
	require.ErrorContains(t, err, "correction applied but crash durability unconfirmed")
	in, err := s.readCorrectionIntent(c.MissionID)
	require.NoError(t, err)
	require.NotNil(t, in)
	a, err := s.readCorrections(c)
	require.NoError(t, err)
	require.Equal(t, []CorrectionEntry{in.Entry}, a.Entries)
	return requiredReplayFixture{store: s, id: c.MissionID, event: in.Event, session: in.EventSession,
		receipt: s.correctionIntentPath(c.MissionID), retry: func(retry *Store) error { return retry.Correct(c.MissionID, correction) }}
}

func acceptedCorrectionAttempt(t *testing.T, f requiredReplayFixture, route string) error {
	t.Helper()
	retry := NewRepositoryStore(f.store.policy, f.store.root).WithSessionID("retry")
	if route == "exact" {
		return f.retry(retry)
	}
	// Update's reconciliation runs before its terminal-state guard. An intact
	// accepted receipt is cleaned, then the ordinary guard still refuses Update.
	c, err := retry.Load(f.id)
	require.NoError(t, err)
	_, err = retry.UpdateWithOutcome(c)
	return err
}

func TestAcceptedCorrectionRecoveryRequiredEvidence(t *testing.T) {
	for _, route := range []string{"exact", "ordinary"} {
		for _, fault := range []string{"missing", "malformed", "session", "divergent-live", "divergent-archive", "divergent-record", "divergent-checkout", "stream-sync", "authority-sync"} {
			t.Run(route+"/"+fault, func(t *testing.T) {
				f := acceptedCorrectionRecovery(t)
				live := (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(f.store.policy)
				paths := []string{f.receipt, f.store.correctionsPath(f.id), mustContractPath(t, f.store, f.id), live}
				switch fault {
				case "missing", "session":
					replaceReplayLive(t, f)
					if fault == "session" {
						paths = append(paths, writeReplayCandidate(t, f, "live", "other", f.event))
					}
				case "malformed":
					b, err := os.ReadFile(live)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(live, append(b, []byte("broken JSON\n")...), 0o600))
				case "stream-sync", "authority-sync":
					real := syncDir
					t.Cleanup(func() { syncDir = real })
					dir := filepath.Dir(live)
					if fault == "authority-sync" {
						dir = filepath.Dir(f.store.correctionsPath(f.id))
					}
					syncDir = func(path string) error {
						if path == dir {
							return errors.New("required recovery sync failed")
						}
						return real(path)
					}
				default:
					changed := f.event
					changed.Actor = "other"
					layer := fault[len("divergent-"):]
					if layer == "live" {
						replaceReplayLive(t, f, changed)
					} else {
						paths = append(paths, writeReplayCandidate(t, f, layer, f.session, changed))
					}
				}
				before := map[string][]byte{}
				for _, path := range paths {
					b, err := os.ReadFile(path)
					require.NoError(t, err)
					before[path] = b
				}
				err := acceptedCorrectionAttempt(t, f, route)
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "not installed")
				assert.NotContains(t, err.Error(), "rolled back")
				assert.NotContains(t, err.Error(), "updates are accepted only", "required recovery must fail before the ordinary guard")
				for path, want := range before {
					got, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, want, got, path)
				}
				assert.NoFileExists(t, (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: "retry"}).LivePath(f.store.policy))
			})
		}
	}
}

func TestAcceptedCorrectionRecoveryCanonicalPayload(t *testing.T) {
	for _, route := range []string{"exact", "ordinary"} {
		for _, layer := range []string{"live", "archive", "record", "checkout"} {
			for _, variant := range replayPayloadVariants() {
				t.Run(route+"/"+layer+"/"+variant.name, func(t *testing.T) {
					f := acceptedCorrectionRecovery(t)
					candidate := installSoleReplayPayload(t, f, layer, replayPayloadBytes(t, f.event, variant))
					before, err := os.ReadFile(candidate)
					require.NoError(t, err)
					err = acceptedCorrectionAttempt(t, f, route)
					if variant.before != "" {
						require.Error(t, err)
						assert.NotContains(t, err.Error(), "updates are accepted only")
						assert.FileExists(t, f.receipt)
					} else {
						if route == "exact" {
							require.NoError(t, err)
						} else {
							require.ErrorContains(t, err, "updates are accepted only")
						}
						assert.NoFileExists(t, f.receipt)
					}
					after, err := os.ReadFile(candidate)
					require.NoError(t, err)
					assert.Equal(t, before, after)
				})
			}
		}
	}
}

func TestAcceptedCorrectionRecoveryEqualCopies(t *testing.T) {
	for _, route := range []string{"exact", "ordinary"} {
		t.Run(route, func(t *testing.T) {
			f := acceptedCorrectionRecovery(t)
			paths := []string{f.store.correctionsPath(f.id), mustContractPath(t, f.store, f.id),
				(audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(f.store.policy)}
			for _, layer := range []string{"archive", "record", "checkout"} {
				paths = append(paths, writeReplayCandidate(t, f, layer, f.session, f.event))
			}
			before := map[string][]byte{}
			for _, path := range paths {
				b, err := os.ReadFile(path)
				require.NoError(t, err)
				before[path] = b
			}
			err := acceptedCorrectionAttempt(t, f, route)
			if route == "exact" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "updates are accepted only")
			}
			assert.NoFileExists(t, f.receipt)
			for path, want := range before {
				got, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, want, got, path)
			}
			assert.NoFileExists(t, (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: "retry"}).LivePath(f.store.policy))
		})
	}
}
