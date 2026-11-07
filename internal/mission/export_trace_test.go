package mission

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
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestFullExportAcceptedHistory(t *testing.T) {
	const stamp = "2026-11-04T10:00:00Z"
	result := Result{Round: 2, Verdict: VerdictPass, Author: "bwk", CreatedAt: stamp}
	resultEvent := Event{Event: "result", Actor: "bwk", TS: stamp, Details: map[string]any{"round": float64(2), "verdict": VerdictPass}}
	closeEvent := Event{Event: "close", Actor: "lead", TS: stamp, Details: map[string]any{"round": float64(2), "verdict": VerdictPass, "status": StatusClosed}}
	abandonEvent := Event{Event: "abandon", Actor: "lead", TS: stamp, Details: map[string]any{"reason": "cancelled"}}
	transitionEvent := exportTransitionEvent(1, RecommendationContinue)
	// Repository appends stamp a monotonic observation time independently of
	// authority's wall-clock timestamp. Both are genuine, not an exact string join.
	resultEvent.TS = "2026-11-04T10:00:01.000000001Z"
	closeEvent.TS = "2026-11-04T10:00:02.000000001Z"
	abandonEvent.TS = "2026-11-04T10:00:03.000000001Z"
	tests := []struct {
		name    string
		status  string
		results []Result
		events  []Event
		want    string
	}{
		{"open without results", StatusOpen, nil, []Event{transitionEvent}, ""},
		{"crossed round needs transition but no result", StatusClosed, []Result{result}, []Event{transitionEvent, resultEvent, closeEvent}, ""},
		{"open accepted result missing", StatusOpen, []Result{result}, nil, "round 2 accepted result event"},
		{"terminal accepted result missing", StatusClosed, []Result{result}, []Event{closeEvent}, "round 2 accepted result event"},
		{"close missing", StatusClosed, []Result{result}, []Event{resultEvent}, "terminal closed event"},
		{"abandon missing", StatusAbandoned, nil, nil, "terminal abandoned event"},
		{"abandon present", StatusAbandoned, nil, []Event{transitionEvent, abandonEvent}, ""},
		{"close cannot replace abandon", StatusAbandoned, nil, []Event{closeEvent}, "terminal abandoned event"},
		{"abandon cannot replace close", StatusClosed, []Result{result}, []Event{resultEvent, abandonEvent}, "terminal closed event"},
		{"all accepted rounds required", StatusOpen, []Result{{Round: 1}, result}, []Event{resultEvent}, "round 1 accepted result event"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := exportTransitionContract(2)
			c.Status, c.ClosedAt = tt.status, stamp
			err := checkFullExportHistory(c, tt.results, tt.events)
			if tt.want == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.want)
			}
		})
	}

	for _, kind := range []string{"result", "close"} {
		for _, field := range []string{"round", "verdict", "actor", "fractional", "string", "overflow", "null", "object", "missing"} {
			t.Run(kind+" mismatched "+field, func(t *testing.T) {
				bad := resultEvent
				want := "accepted result event"
				if kind == "close" {
					bad = closeEvent
					want = "terminal closed event"
				}
				bad.Details = map[string]any{"round": float64(2), "verdict": VerdictPass, "status": StatusClosed}
				switch field {
				case "round":
					bad.Details["round"] = float64(1)
				case "verdict":
					bad.Details["verdict"] = VerdictFail
				case "actor":
					bad.Actor = "other"
				case "fractional":
					bad.Details["round"] = 2.5
				case "string":
					bad.Details["round"] = "2"
				case "overflow":
					bad.Details["round"] = json.Number("9999999999999999999999999")
				case "null":
					bad.Details["round"] = nil
				case "object":
					bad.Details["round"] = map[string]string{"round": "2"}
				case "missing":
					delete(bad.Details, "round")
				}
				events := []Event{bad, closeEvent}
				if kind == "close" {
					events = []Event{resultEvent, bad}
				}
				c := &Contract{MissionID: "m-2026-11-04-001", Status: StatusClosed, CurrentRound: 2, Leader: "lead", ClosedAt: stamp}
				assert.ErrorContains(t, checkFullExportHistory(c, []Result{result}, events), want)
			})
		}
	}
	for _, status := range []string{StatusFailed, StatusEscalated} {
		t.Run("close status "+status, func(t *testing.T) {
			c := &Contract{MissionID: "m-2026-11-04-001", Status: status, CurrentRound: 2, Leader: "lead", ClosedAt: stamp}
			assert.ErrorContains(t, checkFullExportHistory(c, []Result{result}, []Event{resultEvent, closeEvent}), "terminal "+status+" event")
		})
	}
}

func TestFullExportEventEligibilityRuns(t *testing.T) {
	for _, tt := range []struct {
		name  string
		modes []string
		runs  int
	}{
		{"all private", []string{"blame", "blame", "blame"}, 1},
		{"all eligible", []string{"full", "full", "full"}, 1},
		{"private middle", []string{"full", "blame", "full"}, 3},
		{"private prefix and suffix", []string{"blame", "full", "blame"}, 3},
		{"multiple runs", []string{"full", "full", "blame", "blame", "full", "full", "blame"}, 4},
		{"missing consent stays private", []string{"full", "", "blame", "full"}, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, linked := range []bool{false, true} {
				name := "same roots"
				if linked {
					name = "distinct roots"
				}
				t.Run(name, func(t *testing.T) {
					root := t.TempDir()
					checkout := root
					if linked {
						checkout = t.TempDir()
					}
					p, err := layout.NewRepository(root, checkout, layout.RecordBlame)
					require.NoError(t, err)
					s := NewRepositoryStore(p, t.TempDir())
					stream := audit.Stream{Namespace: audit.MissionNS, Mission: "m-2026-11-04-813", Session: "export-runs"}
					var lines []audit.Line
					for i, mode := range tt.modes {
						ts := time.Date(2026, 11, 4, 10, 0, i, 0, time.UTC).UnixNano()
						raw, err := json.Marshal(Event{TS: audit.FormatLineTS(ts), Event: "update", Actor: "bwk", Record: mode,
							Details: map[string]any{"number": json.Number("9007199254740993"), "nested": []string{"payload", "evidence"}}})
						require.NoError(t, err)
						lines = append(lines, audit.Line{TS: ts, Raw: raw, Session: stream.Session})
					}
					writeChunk := func(root string, selected []audit.Line) (string, []byte) {
						t.Helper()
						var body []byte
						for _, line := range selected {
							body = append(body, line.Raw...)
							body = append(body, '\n')
						}
						path := filepath.Join(root, "missions", stream.Mission, audit.MissionChunkFile(stream.Session, selected[0].TS, selected[len(selected)-1].TS))
						require.NoError(t, audit.WriteLocalAtomic(p, path, body))
						return path, body
					}
					local, original := writeChunk(audit.ArchiveRoot(p), lines)
					var eligible []audit.Line
					for _, line := range lines {
						if audit.Eligible(stream, line) {
							eligible = append(eligible, line)
						}
					}
					published := ""
					var publishedBody []byte
					if len(eligible) > 0 {
						published, publishedBody = writeChunk(p.CheckoutRecordRoot(), eligible)
					}
					files, events, err := s.snapshotExportEvents(stream.Mission)
					require.NoError(t, err)
					require.Len(t, files, tt.runs)
					require.Len(t, events, len(lines), "equal archive/publication copies collapse before export")
					want := map[string][]byte{}
					for _, file := range files {
						want[file.Path] = file.Data
					}
					// Install one output at a time. An interruption after any prefix
					// must leave the next snapshot identical, including sparse AC.
					for _, file := range files {
						require.NoError(t, audit.WriteLocalAtomic(p, filepath.Join(p.CheckoutRecordRoot(), "missions", file.Path), file.Data))
						again, _, err := s.snapshotExportEvents(stream.Mission)
						require.NoError(t, err)
						got := map[string][]byte{}
						for _, copy := range again {
							got[copy.Path] = copy.Data
						}
						assert.Equal(t, want, got)
					}
					union, err := audit.ReadStream(p, stream)
					require.NoError(t, err)
					require.Len(t, union.Lines, len(lines))
					for i, line := range union.Lines {
						assert.Equal(t, lines[i].TS, line.TS, "chronological entry identity is unchanged")
						want, err := audit.CanonicalPayload(lines[i].Raw)
						require.NoError(t, err)
						got, err := audit.CanonicalPayload(line.Raw)
						require.NoError(t, err)
						assert.Equal(t, want, got, "exact numbers, evidence and captured consent survive")
					}
					after, err := os.ReadFile(local)
					require.NoError(t, err)
					assert.Equal(t, original, after)
					if published != "" {
						after, err := os.ReadFile(published)
						require.NoError(t, err)
						assert.Equal(t, publishedBody, after)
					}
				})
			}
		})
	}
}

func TestFullExportEventsRedactionAndRefusal(t *testing.T) {
	for _, kind := range []string{"redaction", "malformed event", "conflicting overlap", "unsealed tail"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			p, err := layout.NewRepository(root, root, layout.RecordBlame)
			require.NoError(t, err)
			s := NewRepositoryStore(p, t.TempDir())
			stream := audit.Stream{Namespace: audit.MissionNS, Mission: "m-2026-11-04-814", Session: "export-check"}
			ts := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC).UnixNano()
			event := Event{TS: audit.FormatLineTS(ts), Event: "update", Actor: "bwk", Record: "blame"}
			if kind == "redaction" {
				event.Details = map[string]any{"reason": root + "/notes token=ghp_abcdefghijklmnopqrstuvwxyz0123456789abcd person@example.com"}
			} else if kind == "malformed event" {
				event.Actor = ""
			}
			raw, err := json.Marshal(event)
			require.NoError(t, err)
			body := append(raw, '\n')
			name := audit.MissionChunkFile(stream.Session, ts, ts)
			if kind == "unsealed tail" {
				require.NoError(t, layout.EnsureLocalDir(p, filepath.Dir(stream.LivePath(p))))
				require.NoError(t, os.WriteFile(stream.LivePath(p), body, 0o600))
			} else {
				require.NoError(t, audit.WriteLocalAtomic(p, filepath.Join(audit.ArchiveRoot(p), "missions", stream.Mission, name), body))
			}
			if kind == "conflicting overlap" {
				event.Actor = "other"
				raw, err := json.Marshal(event)
				require.NoError(t, err)
				require.NoError(t, audit.WriteLocalAtomic(p, filepath.Join(p.RecordRoot(), "missions", stream.Mission, name), append(raw, '\n')))
			}
			files, events, err := s.snapshotExportEvents(stream.Mission)
			switch kind {
			case "malformed event":
				assert.ErrorContains(t, err, "invalid mission event")
			case "conflicting overlap":
				assert.ErrorContains(t, err, "identity conflict")
			case "unsealed tail":
				assert.ErrorContains(t, err, "tail changed after sealing")
			case "redaction":
				require.NoError(t, err)
				require.Len(t, files, 1)
				require.Len(t, events, 1)
				assert.NotContains(t, string(files[0].Data), root)
				assert.NotContains(t, string(files[0].Data), "ghp_")
				assert.NotContains(t, string(files[0].Data), "person@example.com")
				assert.Contains(t, string(files[0].Data), "[redacted-secret]")
				assert.True(t, bytes.HasSuffix(files[0].Data, []byte{'\n'}))
				assert.Equal(t, 1, strings.Count(string(files[0].Data), "\n"))
			}
		})
	}
}
