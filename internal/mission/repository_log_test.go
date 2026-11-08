package mission

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

func TestRepositoryEventDecodingPreservesExactNumbers(t *testing.T) {
	decision := RoundDecision{ID: strings.Repeat("a", 32), Round: 1, Author: "bwk", Recommendation: RecommendationContinue}
	for _, decoder := range []struct {
		name   string
		decode func([]byte) (Event, error)
	}{
		{"stream", decodeRepositoryEventLine},
		{"residue", decodeResidueEventLine},
	} {
		t.Run(decoder.name, func(t *testing.T) {
			for _, tt := range []struct {
				number string
				match  bool
			}{
				{"1", true},
				{"2", false},
				{"1.0000000000000001", false},
				{"0.99999999999999999", false},
				{"1.0", false},
				{"1e0", false},
				{"9007199254740993", false},
				{"1e9999", false},
			} {
				t.Run(tt.number, func(t *testing.T) {
					raw := []byte(fmt.Sprintf(`{"ts":"2026-11-04T10:00:00Z","event":"reflect","actor":"bwk","operation_id":%q,"details":{"round":%s,"recommendation":"continue","nested":[9007199254740993]}}`, decision.ID, tt.number))
					event, err := decoder.decode(raw)
					require.NoError(t, err)
					assert.Equal(t, json.Number(tt.number), event.Details["round"])
					assert.Equal(t, []any{json.Number("9007199254740993")}, event.Details["nested"])
					assert.Equal(t, tt.match, matchesReflectionDecision(event, decision))
					encoded, err := json.Marshal(event)
					require.NoError(t, err)
					assert.Contains(t, string(encoded), `"round":`+tt.number)
				})
			}
		})
	}
}

func TestRepositoryEventDecodingRejectsMalformedInput(t *testing.T) {
	const valid = `{"ts":"2026-11-04T10:00:00Z","event":"reflect","actor":"bwk","details":{"round":1}}`
	for _, raw := range []string{
		strings.TrimSuffix(valid, "}"),
		valid + " {}",
		valid + " trailing",
		strings.Replace(valid, `"round":1`, `"round":01`, 1),
		strings.Replace(valid, `"round":1`, `"round":NaN`, 1),
		strings.Replace(valid, "2026-11-04T10:00:00Z", "invalid", 1),
		strings.Replace(valid, `"actor":"bwk"`, `"actor":""`, 1),
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := decodeRepositoryEventLine([]byte(raw))
			assert.Error(t, err)
			_, err = decodeResidueEventLine([]byte(raw))
			assert.Error(t, err)
		})
	}
	// Residue alone tolerates its historical session field.
	residue := strings.TrimSuffix(valid, "}") + `,"session":"original"}`
	_, err := decodeRepositoryEventLine([]byte(residue))
	assert.ErrorContains(t, err, "unknown field")
	_, err = decodeResidueEventLine([]byte(residue))
	assert.NoError(t, err)
}

func TestRepositoryCorrectionEventAuthorityUnavailable(t *testing.T) {
	for _, state := range []string{"missing", "malformed", "directory", "initialized-empty"} {
		t.Run(state, func(t *testing.T) {
			s, c := authorityStore(t, 2)
			require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
			_, err := s.Close(c.MissionID, StatusClosed)
			require.NoError(t, err)
			require.NoError(t, s.Correct(c.MissionID, Correction{Mission: c.MissionID, Author: "claude", Kind: CorrectionDecision, Corrected: "accepted terminal annotation"}))
			events, warnings, err := s.LoadEvents(c.MissionID)
			require.NoError(t, err)
			require.Empty(t, warnings)
			var operation string
			for _, event := range events {
				if event.Event == EventCorrect {
					require.Equal(t, "accepted", event.Acceptance)
					operation = event.OperationID
				}
			}
			require.NotEmpty(t, operation)
			contractPath := mustContractPath(t, s, c.MissionID)
			contractBefore, err := os.ReadFile(contractPath)
			require.NoError(t, err)
			live := (audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "original"}).LivePath(s.policy)
			liveBefore, err := os.ReadFile(live)
			require.NoError(t, err)
			path := s.correctionsPath(c.MissionID)
			var replacement []byte
			switch state {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "malformed":
				replacement = []byte("version: [broken\n")
				require.NoError(t, os.WriteFile(path, replacement, 0o600))
			case "directory":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
			case "initialized-empty":
				replacement, err = yaml.Marshal(correctionsAuthority{Version: 1, Mission: c.MissionID, Entries: []CorrectionEntry{}})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, replacement, 0o600))
			}
			for range 2 {
				events, warnings, err = s.LoadEvents(c.MissionID)
				require.NoError(t, err)
				found := 0
				for _, event := range events {
					if event.OperationID != operation {
						continue
					}
					found++
					if state == "initialized-empty" {
						assert.Equal(t, "uncommitted", event.Acceptance)
						assert.Empty(t, warnings)
					} else {
						assert.Equal(t, "unknown", event.Acceptance)
						assert.Contains(t, strings.Join(warnings, "\n"), "correction acceptance unknown: shared authority")
					}
				}
				assert.Equal(t, 1, found)
			}
			if state == "missing" {
				assert.NoFileExists(t, path, "a log reader must not reconstruct authority")
			} else if state == "directory" {
				assert.DirExists(t, path)
			} else {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, replacement, after)
			}
			for path, before := range map[string][]byte{contractPath: contractBefore, live: liveBefore} {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, before, after, "log reads must not write authority or events")
			}
			assert.NoFileExists(t, s.correctionIntentPath(c.MissionID))
		})
	}
}

func TestRepositoryMissionEventsHonorRetiredFloor(t *testing.T) {
	for _, operation := range []string{"result", "update", "reflect", "correct"} {
		t.Run(operation, func(t *testing.T) {
			s, c := authorityStore(t, 2)
			if operation == "correct" {
				require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
				_, err := s.Close(c.MissionID, StatusClosed)
				require.NoError(t, err)
			}
			// Keep earlier mission history in its original stream. This new
			// stream contains only a quarantine marker, no surviving lines.
			s = s.WithSessionID("retired")
			stream := audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: "retired"}
			floor := time.Now().UTC().Add(24 * time.Hour).UnixNano()
			require.True(t, time.Unix(0, floor).After(time.Now()), "the retired floor must exceed wall clock")
			stem := "log-retired-" + audit.TSToField(floor-1) + "-" + audit.TSToField(floor)
			marker := audit.Marker{Chunk: stem, VerifiedLast: floor, Reason: "verified retired history"}
			data, err := audit.MarshalMarker(marker)
			require.NoError(t, err)
			dir := filepath.Join(s.policy.LocalRoot(), "trace", "missions", c.MissionID)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			markerPath := filepath.Join(dir, stem+".quarantine")
			require.NoError(t, os.WriteFile(markerPath, data, 0o600))
			before, err := audit.ReadStream(s.policy, stream)
			require.NoError(t, err)
			require.Empty(t, before.Lines)
			require.Empty(t, before.Legacy)
			require.Equal(t, floor, before.Floor, "fixture must expose the verified marker-only floor")
			var expectedEvent string
			switch operation {
			case "result":
				expectedEvent = "result"
				require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
			case "update":
				expectedEvent = "update"
				loaded, err := s.Load(c.MissionID)
				require.NoError(t, err)
				loaded.Inputs.References = []string{"ordinary-metadata-update"}
				require.NoError(t, s.Update(loaded))
			case "reflect":
				expectedEvent = "reflect"
				s.persistenceCheck = func(phase string) error {
					if phase == "event_prepared" {
						return errors.New("interrupt after durable preparation")
					}
					return nil
				}
				_, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
				require.Error(t, err)
				in, err := s.readRoundIntent(c.MissionID, 1)
				require.NoError(t, err)
				require.NotNil(t, in)
				ts, err := audit.ParseLineTS(in.Event.TS)
				require.NoError(t, err)
				assert.Greater(t, ts, floor)
				s.persistenceCheck = func(phase string) error {
					if phase == "reflect_event_delivered" {
						return errors.New("interrupt after required event delivery")
					}
					return nil
				}
				_, err = s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
				require.Error(t, err)
				delivered, err := audit.ReadStream(s.policy, stream)
				require.NoError(t, err)
				require.Len(t, delivered.Lines, 1)
				assert.Equal(t, ts, delivered.Lines[0].TS, "retry must keep the durably prepared timestamp")
				s.persistenceCheck = nil
				out, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
				require.NoError(t, err)
				assert.Equal(t, "applied", out.Application)
				// A second exact request must reuse the event, not allocate again.
				out, err = s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
				require.NoError(t, err)
				assert.Equal(t, "already_applied", out.Application)
			case "correct":
				expectedEvent = EventCorrect
				require.NoError(t, s.Correct(c.MissionID, Correction{Mission: c.MissionID, Author: "claude", Kind: CorrectionDecision, Corrected: "annotation after retired history"}))
			}
			after, err := audit.ReadStream(s.policy, stream)
			require.NoError(t, err)
			require.Len(t, after.Lines, 1)
			assert.Greater(t, after.Lines[0].TS, floor)
			event, err := decodeEventLine(after.Lines[0].Raw)
			require.NoError(t, err)
			assert.Equal(t, expectedEvent, event.Event)
			unchanged, err := os.ReadFile(markerPath)
			require.NoError(t, err)
			assert.Equal(t, data, unchanged)
		})
	}
}
