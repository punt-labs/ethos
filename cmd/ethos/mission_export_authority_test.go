//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func fullExportAuthorityFixture(t *testing.T) (*cliSubprocessEnv, *mission.Store, string) {
	t.Helper()
	se, s, id := fullExportFixture(t)
	require.NoError(t, s.AppendResult(id, &mission.Result{
		Mission: id, Round: 1, Author: "bwk", Verdict: mission.VerdictPass, Confidence: 0.8,
		Evidence: []mission.EvidenceCheck{{Name: "focused check", Status: mission.EvidenceStatusPass}},
	}))
	_, err := s.Close(id, mission.StatusClosed)
	require.NoError(t, err)
	require.NoError(t, s.Correct(id, mission.Correction{
		Mission: id, Round: 1, Author: "claude", Kind: mission.CorrectionFactual,
		Claim: "original claim", Corrected: "accepted corrected claim", Supersedes: "previous annotation",
		Evidence: []mission.EvidenceCheck{{Name: "accepted evidence", Status: mission.EvidenceStatusPass}},
	}))
	return se, s, id
}

func TestMissionExportFullRequiresAuthorityPayload(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		field string
		value any
	}{
		{"reflection actor", "reflect", "actor", "other"},
		{"reflection round", "reflect", "round", 2},
		{"reflection recommendation", "reflect", "recommendation", mission.RecommendationStop},
		{"reflection fractional round", "reflect", "round", 1.5},
		{"reflection precise fractional round", "reflect", "round", json.Number("1.0000000000000001")},
		{"reflection precise fractional round below one", "reflect", "round", json.Number("0.99999999999999999")},
		{"reflection decimal round", "reflect", "round", json.Number("1.0")},
		{"reflection exponent round", "reflect", "round", json.Number("1e0")},
		{"reflection string round", "reflect", "round", "1"},
		{"reflection overflow round", "reflect", "round", json.Number("9999999999999999999999999")},
		{"reflection null round", "reflect", "round", nil},
		{"reflection object round", "reflect", "round", map[string]int{"round": 1}},
		{"reflection missing round", "reflect", "missing", "round"},
		{"reflection round alias", "reflect", "alias", "round"},
		{"reflection recommendation alias", "reflect", "alias", "recommendation"},
		{"reflection null recommendation", "reflect", "recommendation", nil},
		{"correction actor", mission.EventCorrect, "actor", "other"},
		{"correction round", mission.EventCorrect, "round", 0},
		{"correction precise fractional round", mission.EventCorrect, "round", json.Number("1.0000000000000001")},
		{"correction kind", mission.EventCorrect, "kind", string(mission.CorrectionFabrication)},
		{"correction claim", mission.EventCorrect, "claim", "divergent claim"},
		{"correction corrected", mission.EventCorrect, "corrected", "divergent corrected claim"},
		{"correction supersedes", mission.EventCorrect, "supersedes", "divergent annotation"},
		{"correction evidence", mission.EventCorrect, "evidence", []mission.EvidenceCheck{{Name: "divergent evidence", Status: mission.EvidenceStatusPass}}},
		{"correction missing corrected", mission.EventCorrect, "missing", "corrected"},
		{"correction extra detail", mission.EventCorrect, "extra", "not accepted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se, s, id := fullExportAuthorityFixture(t)
			p, ok := s.RepositoryPolicy()
			require.True(t, ok)
			// Change only a raw event, preserving its accepted operation ID and
			// event kind. The complete reflection and shared authority stay intact.
			live := (audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "export-session"}).LivePath(p)
			data, err := os.ReadFile(live)
			require.NoError(t, err)
			lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
			var operation string
			for i, line := range lines {
				var event mission.Event
				require.NoError(t, json.Unmarshal(line, &event))
				if event.Event != tt.kind {
					continue
				}
				require.Empty(t, operation, "fixture must have one candidate of this kind")
				operation = event.OperationID
				require.NotEmpty(t, operation)
				switch tt.field {
				case "actor":
					event.Actor = tt.value.(string)
				case "missing":
					delete(event.Details, tt.value.(string))
				case "alias":
					key := tt.value.(string)
					event.Details[strings.ToUpper(key)] = event.Details[key]
					delete(event.Details, key)
				default:
					event.Details[tt.field] = tt.value
				}
				lines[i], err = json.Marshal(event)
				require.NoError(t, err)
			}
			require.NotEmpty(t, operation)
			require.NoError(t, os.WriteFile(live, append(bytes.Join(lines, []byte{'\n'}), '\n'), 0o600))
			retained := map[string][]byte{}
			for _, path := range []string{
				filepath.Join(p.RecordRoot(), "missions", id, "contract.yaml"),
				filepath.Join(p.RecordRoot(), "missions", id, "corrections.yaml"),
				filepath.Join(p.LocalRoot(), "trace", "missions", id, "reflection-1.yaml"),
				filepath.Join(se.repo, ".git", "index"),
			} {
				retained[path], err = os.ReadFile(path)
				require.NoError(t, err)
			}
			want := "required reflection event is unavailable"
			if tt.kind == mission.EventCorrect {
				want = "required correction event is unavailable"
			}
			for range 2 { // Retry after the CLI has sealed the altered tail locally.
				events, warnings, err := s.LoadEvents(id)
				require.NoError(t, err)
				require.Empty(t, warnings)
				found := 0
				for _, event := range events {
					if event.OperationID == operation {
						found++
						assert.Equal(t, tt.kind, event.Event)
						assert.Equal(t, "uncommitted", event.Acceptance)
						if number, ok := tt.value.(json.Number); ok && tt.field == "round" {
							assert.Equal(t, number, event.Details["round"], "the repository reader must preserve the exact numeric token")
						}
					}
				}
				require.Equal(t, 1, found)
				if number, ok := tt.value.(json.Number); ok && tt.field == "round" {
					stdout, stderr, exit := runCLI(t, se, "mission", "log", id, "--event", tt.kind, "--json")
					require.Equal(t, 0, exit, stderr)
					var payload mission.LogPayload
					decoder := json.NewDecoder(strings.NewReader(stdout))
					decoder.UseNumber()
					require.NoError(t, decoder.Decode(&payload))
					require.Len(t, payload.Events, 1)
					assert.Equal(t, "uncommitted", payload.Events[0].Acceptance)
					assert.Equal(t, number, payload.Events[0].Details["round"], "CLI log must not round the token before or after sealing")
				}
				_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "incomplete")
				assert.NotEqual(t, 0, exit, stderr)
				assert.Contains(t, stderr, "full export incomplete")
				assert.Contains(t, stderr, want)
				assert.NoDirExists(t, filepath.Join(se.repo, "incomplete"))
				pending, err := audit.UnarchivedCount(p, audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "export-session"})
				require.NoError(t, err)
				require.Zero(t, pending, "the retry must read the locally sealed candidate")
			}
			for path, before := range retained {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, before, after, "export must not repair authority, detail or the index: %s", path)
			}
		})
	}
}

func TestMissionExportFullRejectsOperationReuse(t *testing.T) {
	for _, kind := range []string{"reflect", mission.EventCorrect} {
		for _, variant := range []string{"same-session-divergent-payload", "same-session-different-timestamp", "cross-session-divergent-payload", "cross-session-identical-payload"} {
			for _, order := range []string{"duplicate-first", "duplicate-last"} {
				t.Run(kind+"/"+variant+"/"+order, func(t *testing.T) {
					se, s, id := fullExportAuthorityFixture(t)
					p, ok := s.RepositoryPolicy()
					require.True(t, ok)
					stream := audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "export-session"}
					live := stream.LivePath(p)
					data, err := os.ReadFile(live)
					require.NoError(t, err)
					lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
					var candidate mission.Event
					var index int
					for i, line := range lines {
						var event mission.Event
						require.NoError(t, json.Unmarshal(line, &event))
						if event.Event == kind {
							candidate, index = event, i
							break
						}
					}
					require.NotEmpty(t, candidate.OperationID)
					if strings.HasSuffix(variant, "divergent-payload") {
						candidate.Actor = "other"
					}
					if strings.HasPrefix(variant, "cross-session") {
						// Stream enumeration visits the duplicate before/after the
						// original; identical bytes in another stream are not a copy.
						stream.Session = "aaa-duplicate"
						if order == "duplicate-last" {
							stream.Session = "zzz-duplicate"
						}
						encoded, err := json.Marshal(candidate)
						require.NoError(t, err)
						require.NoError(t, os.WriteFile(stream.LivePath(p), append(encoded, '\n'), 0o600))
					} else {
						stamp, err := time.Parse(time.RFC3339Nano, candidate.TS)
						require.NoError(t, err)
						offset := time.Nanosecond
						insert := index + 1
						if order == "duplicate-first" {
							offset, insert = -time.Nanosecond, index
						}
						candidate.TS = stamp.Add(offset).UTC().Format(time.RFC3339Nano)
						encoded, err := json.Marshal(candidate)
						require.NoError(t, err)
						updated := append([][]byte{}, lines[:insert]...)
						updated = append(updated, encoded)
						updated = append(updated, lines[insert:]...)
						require.NoError(t, os.WriteFile(live, append(bytes.Join(updated, []byte{'\n'}), '\n'), 0o600))
					}
					beforeIndex, err := os.ReadFile(filepath.Join(se.repo, ".git", "index"))
					require.NoError(t, err)
					for range 2 {
						_, err := audit.FindOperation(p, id, candidate.OperationID)
						require.ErrorContains(t, err, "divergent payloads or sessions", "fixture must exercise operation identity, not timestamp identity")
						_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "incomplete")
						assert.NotEqual(t, 0, exit, stderr)
						assert.Contains(t, stderr, "full export incomplete")
						assert.Contains(t, stderr, "divergent payloads or sessions")
						assert.NoDirExists(t, filepath.Join(se.repo, "incomplete"))
					}
					afterIndex, err := os.ReadFile(filepath.Join(se.repo, ".git", "index"))
					require.NoError(t, err)
					assert.Equal(t, beforeIndex, afterIndex)
				})
			}
		}
	}
}

func TestMissionExportFullAcceptsEqualOperationCopies(t *testing.T) {
	se, s, id := fullExportAuthorityFixture(t)
	_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "complete")
	require.Equal(t, 0, exit, stderr)
	p, ok := s.RepositoryPolicy()
	require.True(t, ok)
	archive := filepath.Join(p.LocalRoot(), "trace", "missions", id)
	chunks, err := filepath.Glob(filepath.Join(archive, "log-*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
	for _, chunk := range chunks {
		data, err := os.ReadFile(chunk)
		require.NoError(t, err)
		published := filepath.Join(p.RecordRoot(), "missions", id, filepath.Base(chunk))
		require.NoError(t, os.WriteFile(published, data, 0o600))
	}
	// The archived and published copies keep their original session and exact
	// payload. They must collapse, not be confused with operation reuse.
	for range 2 {
		_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "complete")
		require.Equal(t, 0, exit, stderr)
	}
}

func TestMissionExportFullAuthorityTimestampsIndependent(t *testing.T) {
	se, s, id := fullExportAuthorityFixture(t)
	c, err := s.Load(id)
	require.NoError(t, err)
	require.Len(t, c.RoundDecisions.Entries, 1)
	events, warnings, err := s.LoadEvents(id)
	require.NoError(t, err)
	require.Empty(t, warnings)
	var operations []string
	for _, event := range events {
		if event.OperationID == "" {
			continue
		}
		require.Equal(t, "accepted", event.Acceptance)
		operations = append(operations, event.OperationID)
		if event.Event == "reflect" {
			assert.NotEqual(t, c.RoundDecisions.Entries[0].CreatedAt, event.TS,
				"the fixture must distinguish event append time from reflection authority time")
		}
	}
	require.Len(t, operations, 2)
	for range 2 {
		_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "complete")
		require.Equal(t, 0, exit, stderr)
	}
	assert.FileExists(t, filepath.Join(se.repo, "complete", id, "reflections.yaml"))
	chunks, err := filepath.Glob(filepath.Join(se.repo, "complete", id, "log-*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
	var exported []byte
	for _, chunk := range chunks {
		data, err := os.ReadFile(chunk)
		require.NoError(t, err)
		exported = append(exported, data...)
	}
	for _, operation := range operations {
		assert.Contains(t, string(exported), operation)
	}
}
