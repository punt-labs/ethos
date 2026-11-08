package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

type requiredReplayFixture struct {
	store   *Store
	id      string
	event   Event
	session string
	receipt string
	retry   func(*Store) error
}

// Leave the exact required event delivered, its receipt retained and shared
// authority uninstalled. Correction has no post-delivery fault seam: interrupt
// preparation, then deliver that receipt under the normal mission/stream locks
// without running the authority installation that would follow it in Correct.
func interruptedRequiredReplay(t *testing.T, kind string) requiredReplayFixture {
	t.Helper()
	s, c := authorityStore(t, 3)
	f := requiredReplayFixture{store: s, id: c.MissionID}
	interrupted := errors.New("interrupted required operation")
	if kind == "reflection" {
		s.persistenceCheck = func(phase string) error {
			if phase == "reflect_event_delivered" {
				return interrupted
			}
			return nil
		}
		out, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
		require.ErrorIs(t, err, interrupted)
		require.Equal(t, "not_applied", out.Application)
		in, err := s.readRoundIntent(c.MissionID, 1)
		require.NoError(t, err)
		require.NotNil(t, in)
		f.event, f.session, f.receipt = in.Event, in.EventSession, s.intentPath(c.MissionID, 1)
		f.retry = func(retry *Store) error {
			_, err := retry.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
			return err
		}
	} else {
		require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
		_, err := s.Close(c.MissionID, StatusClosed)
		require.NoError(t, err)
		correction := Correction{Mission: c.MissionID, Round: 1, Author: "claude", Kind: CorrectionDecision, Corrected: "retry this exact annotation"}
		s.persistenceCheck = func(phase string) error {
			if phase == "event_prepared" {
				return interrupted
			}
			return nil
		}
		require.ErrorIs(t, s.Correct(c.MissionID, correction), interrupted)
		in, err := s.readCorrectionIntent(c.MissionID)
		require.NoError(t, err)
		require.NotNil(t, in)
		s.persistenceCheck = nil
		require.NoError(t, s.withMissionLock(c.MissionID, true, func() error {
			return s.deliverOperation(c.MissionID, in.EventSession, &in.Event, func() error { return s.persistCorrectionIntent(*in) })
		}))
		f.event, f.session, f.receipt = in.Event, in.EventSession, s.correctionIntentPath(c.MissionID)
		f.retry = func(retry *Store) error { return retry.Correct(c.MissionID, correction) }
	}
	s.persistenceCheck = nil
	line, err := audit.FindOperation(s.policy, f.id, f.event.OperationID)
	require.NoError(t, err)
	require.NotNil(t, line)
	decoded, err := decodeEventLine(line.Raw)
	require.NoError(t, err)
	require.True(t, sameJSON(f.event, decoded))
	require.Equal(t, f.session, line.Session)
	loaded, err := s.Load(f.id)
	require.NoError(t, err)
	require.Empty(t, loaded.RoundDecisions.Entries)
	corrections, err := s.readCorrections(loaded)
	require.NoError(t, err)
	require.NotNil(t, corrections)
	require.Empty(t, corrections.Entries)
	return f
}

func replayEventBytes(t *testing.T, event Event) []byte {
	t.Helper()
	b, err := json.Marshal(event)
	require.NoError(t, err)
	return append(b, '\n')
}

// Replace only this operation's live candidates, preserving earlier events.
func replaceReplayLive(t *testing.T, f requiredReplayFixture, events ...Event) string {
	t.Helper()
	path := (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(f.store.policy)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var out []byte
	for _, raw := range bytes.Split(b, []byte{'\n'}) {
		if len(raw) == 0 {
			continue
		}
		event, err := decodeEventLine(raw)
		require.NoError(t, err)
		if event.OperationID != f.event.OperationID {
			out = append(out, raw...)
			out = append(out, '\n')
		}
	}
	for _, event := range events {
		out = append(out, replayEventBytes(t, event)...)
	}
	require.NoError(t, os.WriteFile(path, out, 0o600))
	return path
}

func writeReplayCandidate(t *testing.T, f requiredReplayFixture, layer, session string, event Event) string {
	t.Helper()
	s := f.store
	var path string
	if layer == "live" {
		path = (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: session}).LivePath(s.policy)
	} else {
		ts, err := audit.ParseLineTS(event.TS)
		require.NoError(t, err)
		root := audit.ArchiveRoot(s.policy)
		switch layer {
		case "record":
			root = s.policy.RecordRoot()
		case "checkout":
			root = s.policy.CheckoutRecordRoot()
		}
		path = filepath.Join(root, "missions", f.id, audit.MissionChunkFile(session, ts, ts))
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, replayEventBytes(t, event), 0o600))
	return path
}

func TestRequiredOperationReplayRejectsConflicts(t *testing.T) {
	for _, kind := range []string{"reflection", "correction"} {
		for _, tt := range []struct {
			name    string
			layer   string
			session string
			offset  int64
			reverse bool
		}{
			{"live-later-exact-first", "live", "original", 1, false},
			{"live-later-conflict-first", "live", "original", 1, true},
			{"live-earlier-exact-first", "live", "original", -1, false},
			{"live-earlier-conflict-first", "live", "original", -1, true},
			{"other-session-first-equal-bytes", "live", "aaa", 0, false},
			{"other-session-last-equal-bytes", "live", "zzz", 0, false},
			{"other-session-first-later-payload", "live", "aaa", 1, false},
			{"other-session-last-later-payload", "live", "zzz", 1, false},
			{"archive-later", "archive", "original", 1, false},
			{"archive-earlier", "archive", "original", -1, false},
			{"record-later", "record", "original", 1, false},
			{"record-earlier", "record", "original", -1, false},
			{"checkout-later", "checkout", "original", 1, false},
			{"checkout-earlier", "checkout", "original", -1, false},
		} {
			t.Run(kind+"/"+tt.name, func(t *testing.T) {
				f := interruptedRequiredReplay(t, kind)
				conflict := f.event
				if tt.offset != 0 {
					ts, err := audit.ParseLineTS(conflict.TS)
					require.NoError(t, err)
					conflict.TS = audit.FormatLineTS(ts + tt.offset)
					conflict.Actor = "other"
				}
				var candidate string
				if tt.layer == "live" && tt.session == f.session {
					if tt.reverse {
						candidate = replaceReplayLive(t, f, conflict, f.event)
					} else {
						candidate = replaceReplayLive(t, f, f.event, conflict)
					}
				} else {
					candidate = writeReplayCandidate(t, f, tt.layer, tt.session, conflict)
				}
				// Fixtures must be valid streams; the conflict is operation identity,
				// not a malformed chunk or a duplicate timestamp with different bytes.
				_, err := audit.ReadStream(f.store.policy, audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: tt.session})
				require.NoError(t, err)
				assertReplayRefusedUnchanged(t, f, candidate)
			})
		}
	}
}

func assertReplayRefusedUnchanged(t *testing.T, f requiredReplayFixture, candidate string) {
	t.Helper()
	paths := []string{mustContractPath(t, f.store, f.id), f.store.correctionsPath(f.id), f.receipt, candidate,
		(audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(f.store.policy)}
	before := make(map[string][]byte)
	for _, path := range paths {
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		before[path] = b
	}
	retry := NewRepositoryStore(f.store.policy, f.store.root).WithSessionID("retry")
	assert.Error(t, f.retry(retry), "conflicting replay must not install authority")
	for path, want := range before {
		got, err := os.ReadFile(path)
		if assert.NoError(t, err, "retain %s", path) {
			assert.Equal(t, want, got, "refusal must not mutate %s", path)
		}
	}
	assert.NoFileExists(t, f.store.reflectionArchivePath(f.id, 1))
	assert.NoFileExists(t, (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: "retry"}).LivePath(f.store.policy))
}

func TestRequiredOperationReplayMatchesExactReceipt(t *testing.T) {
	for _, kind := range []string{"reflection", "correction"} {
		for _, fault := range []string{"different-session-only", "different-payload-only", "malformed-event"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f := interruptedRequiredReplay(t, kind)
				replaceReplayLive(t, f)
				changed := f.event
				session := f.session
				switch fault {
				case "different-session-only":
					session = "other"
				case "different-payload-only":
					changed.Actor = "other"
				case "malformed-event":
					changed.Actor = ""
				}
				var candidate string
				if session == f.session {
					candidate = replaceReplayLive(t, f, changed)
				} else {
					candidate = writeReplayCandidate(t, f, "live", session, changed)
				}
				assertReplayRefusedUnchanged(t, f, candidate)
			})
		}
	}
}

func TestRequiredOperationReplayAcceptsEqualCopies(t *testing.T) {
	for _, kind := range []string{"reflection", "correction"} {
		for _, source := range []string{"all-layers", "archive-only", "record-only", "checkout-only"} {
			t.Run(kind+"/"+source, func(t *testing.T) {
				f := interruptedRequiredReplay(t, kind)
				for _, layer := range []string{"archive", "record", "checkout"} {
					if source == "all-layers" || source == layer+"-only" {
						writeReplayCandidate(t, f, layer, f.session, f.event)
					}
				}
				if source != "all-layers" {
					replaceReplayLive(t, f)
				}
				live := (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(f.store.policy)
				liveBefore, err := os.ReadFile(live)
				require.NoError(t, err)
				retry := NewRepositoryStore(f.store.policy, f.store.root).WithSessionID("retry")
				require.NoError(t, f.retry(retry))
				liveAfter, err := os.ReadFile(live)
				require.NoError(t, err)
				assert.Equal(t, liveBefore, liveAfter, "an equal delivered copy must not append again")
				assert.NoFileExists(t, f.receipt)
				assert.NoFileExists(t, (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: "retry"}).LivePath(f.store.policy))
				line, err := audit.FindOperation(f.store.policy, f.id, f.event.OperationID)
				require.NoError(t, err)
				require.NotNil(t, line)
				event, err := decodeEventLine(line.Raw)
				require.NoError(t, err)
				assert.True(t, sameJSON(f.event, event), "preserve timestamp, payload and consent")
				assert.Equal(t, f.session, line.Session)
				loaded, err := retry.Load(f.id)
				require.NoError(t, err)
				if kind == "reflection" {
					require.Len(t, loaded.RoundDecisions.Entries, 1)
					assert.Equal(t, f.event.OperationID, loaded.RoundDecisions.Entries[0].ID)
				} else {
					authority, err := retry.readCorrections(loaded)
					require.NoError(t, err)
					require.Len(t, authority.Entries, 1)
					assert.Equal(t, f.event.OperationID, authority.Entries[0].ID)
				}
				events, warnings, err := retry.LoadEvents(f.id)
				require.NoError(t, err)
				assert.Empty(t, warnings)
				count := 0
				for _, event := range events {
					if event.OperationID == f.event.OperationID {
						count++
					}
				}
				assert.Equal(t, 1, count, "equal copies represent one logical required event")
			})
		}
	}
}

type replayPayloadVariant struct {
	name   string
	before string
	after  string
}

func replayPayloadVariants() []replayPayloadVariant {
	return []replayPayloadVariant{
		{"precise-round-above", `"round":1`, `"round":1.0000000000000001`},
		{"precise-round-below", `"round":1`, `"round":0.99999999999999999`},
		{"decimal-round", `"round":1`, `"round":1.0`},
		{"exponent-round", `"round":1`, `"round":1e0`},
		{"actor-key-case", `"actor":`, `"Actor":`},
		{"details-key-case", `"details":`, `"Details":`},
		{"operation-key-case", `"operation_id":`, `"Operation_ID":`},
		{"timestamp-key-case", `"ts":`, `"TS":`},
		{"canonical-copy", "", ""},
	}
}

func replayPayloadBytes(t *testing.T, event Event, variant replayPayloadVariant) []byte {
	t.Helper()
	raw := bytes.TrimSpace(replayEventBytes(t, event))
	original, err := audit.CanonicalPayload(raw)
	require.NoError(t, err)
	if variant.before != "" {
		require.Equal(t, 1, bytes.Count(raw, []byte(variant.before)))
		raw = bytes.Replace(raw, []byte(variant.before), []byte(variant.after), 1)
	} else {
		// Reorder the envelope keys and add whitespace without changing the
		// canonical identity. Keep this a single complete JSONL line.
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &fields))
		reordered, err := json.MarshalIndent(fields, "", "  ")
		require.NoError(t, err)
		reordered = bytes.ReplaceAll(reordered, []byte{'\n'}, []byte{' '})
		require.NotEqual(t, string(raw), string(reordered))
		raw = reordered
	}
	canonical, err := audit.CanonicalPayload(raw)
	require.NoError(t, err)
	if variant.before == "" {
		require.Equal(t, string(original), string(canonical))
	} else {
		require.NotEqual(t, string(original), string(canonical))
	}
	return append(raw, '\n')
}

// Install exactly one candidate for this operation, with unchanged session,
// timestamp and operation ID. Never deserialize/reserialize its numeric token.
func installSoleReplayPayload(t *testing.T, f requiredReplayFixture, layer string, raw []byte) string {
	t.Helper()
	live := replaceReplayLive(t, f)
	path := live
	if layer == "live" {
		before, err := os.ReadFile(live)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(live, append(before, raw...), 0o600))
	} else {
		path = writeReplayCandidate(t, f, layer, f.session, f.event)
		require.NoError(t, os.WriteFile(path, raw, 0o600))
	}
	data, err := audit.ReadStream(f.store.policy, audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session})
	require.NoError(t, err)
	count := 0
	for _, line := range append(data.Lines, data.Legacy...) {
		event, err := decodeRepositoryEventLine(line.Raw)
		require.NoError(t, err)
		if event.OperationID == f.event.OperationID {
			count++
			require.Equal(t, f.event.TS, event.TS)
			require.Equal(t, f.session, line.Session)
		}
	}
	require.Equal(t, 1, count, "the receipt mismatch must not depend on multiple candidates")
	line, err := audit.FindOperation(f.store.policy, f.id, f.event.OperationID)
	require.NoError(t, err, "all-candidate validation alone cannot detect this receipt mismatch")
	require.NotNil(t, line)
	return path
}

func TestRequiredOperationReplaySoleCandidateCanonicalPayload(t *testing.T) {
	for _, kind := range []string{"reflection", "correction"} {
		for _, layer := range []string{"live", "archive", "record", "checkout"} {
			for _, variant := range replayPayloadVariants() {
				t.Run(kind+"/"+layer+"/"+variant.name, func(t *testing.T) {
					f := interruptedRequiredReplay(t, kind)
					candidate := installSoleReplayPayload(t, f, layer, replayPayloadBytes(t, f.event, variant))
					if variant.before != "" {
						assertReplayRefusedUnchanged(t, f, candidate)
						return
					}
					live := (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: f.session}).LivePath(f.store.policy)
					before, err := os.ReadFile(live)
					require.NoError(t, err)
					retry := NewRepositoryStore(f.store.policy, f.store.root).WithSessionID("retry")
					require.NoError(t, f.retry(retry), "canonical equality ignores key order and whitespace")
					assert.NoFileExists(t, f.receipt)
					after, err := os.ReadFile(live)
					require.NoError(t, err)
					assert.Equal(t, before, after, "a canonical copy must not append again")
					assert.NoFileExists(t, (audit.Stream{Namespace: audit.MissionNS, Mission: f.id, Session: "retry"}).LivePath(f.store.policy))
				})
			}
		}
	}
}
