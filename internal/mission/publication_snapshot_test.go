package mission

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

const publicationID = "m-2026-11-04-001"
const publicationStamp = "2026-11-04T10:00:00Z"

func publicationWrite(t *testing.T, p string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, data, 0o600))
}

func publicationYAML[T any](t *testing.T, v T) []byte {
	t.Helper()
	b, err := yaml.Marshal(v)
	require.NoError(t, err)
	return b
}

func publicationStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	p, err := layout.NewRepository(root, t.TempDir(), layout.RecordBlame)
	require.NoError(t, err)
	s := NewRepositoryStore(p, t.TempDir())
	dir := filepath.Join(p.RecordRoot(), "missions", publicationID)
	c := Contract{MissionID: publicationID, Status: StatusClosed, CreatedAt: publicationStamp, UpdatedAt: publicationStamp, ClosedAt: publicationStamp,
		Leader: "lead", Worker: "bwk", Evaluator: Evaluator{Handle: "rsc", PinnedAt: publicationStamp}, WriteSet: []string{"source.go"}, SuccessCriteria: []string{"checked"}, Budget: Budget{Rounds: 2}, CurrentRound: 2,
		RoundDecisions: &RoundDecisions{Version: 1, Entries: []RoundDecision{{ID: strings.Repeat("a", 32), Round: 1, Author: "lead", CreatedAt: publicationStamp, Recommendation: "continue"}}}}
	publicationWrite(t, filepath.Join(dir, "contract.yaml"), publicationYAML(t, c))
	rs := resultsFile{}
	for round := 1; round <= 2; round++ {
		rs.Results = append(rs.Results, Result{Mission: publicationID, Round: round, Author: "bwk", Verdict: "pass", Confidence: 1, Evidence: []EvidenceCheck{{Name: "check", Status: "pass"}}})
	}
	publicationWrite(t, filepath.Join(dir, "results.yaml"), publicationYAML(t, rs))
	ca := correctionsAuthority{Version: 1, Mission: publicationID, Entries: []CorrectionEntry{{ID: strings.Repeat("b", 32), CreatedAt: publicationStamp, Correction: Correction{Mission: publicationID, Round: 1, Kind: CorrectionFactual, Author: "lead", Claim: "old claim", Corrected: "corrected claim"}}}}
	publicationWrite(t, filepath.Join(dir, "corrections.yaml"), publicationYAML(t, ca))
	for _, did := range []string{"d-a", "d-b"} {
		publicationWrite(t, filepath.Join(dir, "delegations", did, "record.yaml"), publicationYAML(t, Delegation{ID: did, Mission: publicationID, Tier: TierB, AgentType: did, CreatedAt: publicationStamp, Verdict: "pass"}))
		publicationWrite(t, filepath.Join(dir, "delegations", did, "prompt.md"), []byte("private prompt"))
	}
	publicationWrite(t, filepath.Join(dir, "reflections.yaml"), []byte("private reflections"))
	publicationWrite(t, filepath.Join(dir, "log-2026.jsonl"), []byte("private events"))
	publicationWrite(t, filepath.Join(dir, "artifacts", "private.md"), []byte("private artifact"))
	row, err := json.Marshal(TraceSummary{ID: publicationID, Status: c.Status, ClosedAt: c.ClosedAt, RoundsUsed: c.CurrentRound})
	require.NoError(t, err)
	publicationWrite(t, filepath.Join(p.RecordRoot(), "missions.jsonl"), append(row, '\n'))
	return s, dir
}

func TestPublicationSnapshotCompleteSelectedAndImmutable(t *testing.T) {
	s, dir := publicationStore(t)
	for _, a := range []adr.ADR{
		{ID: "DES-001", Title: "linked", Status: "settled", CreatedAt: publicationStamp, UpdatedAt: publicationStamp, Decision: "linked decision", MissionID: publicationID},
		{ID: "DES-002", Title: "explicit", Status: "settled", CreatedAt: publicationStamp, UpdatedAt: publicationStamp, Decision: "explicit decision"},
		{ID: "DES-003", Title: "unselected", Status: "settled", CreatedAt: publicationStamp, UpdatedAt: publicationStamp, Decision: "not selected"},
	} {
		publicationWrite(t, filepath.Join(s.policy.RecordRoot(), "adrs", a.ID+".yaml"), publicationYAML(t, a))
	}
	snapshot, err := s.SnapshotSelection(context.Background(), []string{publicationID}, []string{"DES-002"})
	require.NoError(t, err)
	require.NoError(t, ValidatePublicationSnapshot(snapshot))
	require.Len(t, snapshot.Records, 7)
	var paths []string
	var contract []byte
	for _, record := range snapshot.Records {
		paths = append(paths, record.Path)
		assert.NotContains(t, record.Path, "prompt.md")
		assert.NotContains(t, record.Path, "reflections")
		assert.NotContains(t, record.Path, "log-")
		assert.NotContains(t, record.Path, "artifacts")
		if strings.HasSuffix(record.Path, "contract.yaml") {
			contract = bytes.Clone(record.Data)
			assert.Contains(t, string(contract), "round_decisions:")
		}
		if strings.HasSuffix(record.Path, "results.yaml") {
			var rf resultsFile
			require.NoError(t, publicationDecode(record.Data, &rf))
			assert.Len(t, rf.Results, 2)
		}
	}
	assert.Contains(t, paths, ".punt-labs/ethos/adrs/DES-001.yaml")
	assert.Contains(t, paths, ".punt-labs/ethos/adrs/DES-002.yaml")
	assert.NotContains(t, paths, ".punt-labs/ethos/adrs/DES-003.yaml")
	publicationWrite(t, filepath.Join(dir, "contract.yaml"), []byte("changed later"))
	for _, record := range snapshot.Records {
		if strings.HasSuffix(record.Path, "contract.yaml") {
			assert.Equal(t, contract, record.Data)
		}
	}
}

func TestPublicationSnapshotRefusesIncompleteUnknownAndMalformed(t *testing.T) {
	for _, kind := range []string{"unknown", "missing-results", "missing-delegation", "unknown-delegation", "missing-corrections", "bad-corrections", "null-corrections", "missing-decisions", "unknown-contract", "multiple-results", "summary", "missing-summary", "adr", "explicit-adr", "symlink", "parent-symlink", "results-directory"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := publicationStore(t)
			var adrs []string
			switch kind {
			case "unknown":
				publicationWrite(t, filepath.Join(dir, "future.yaml"), []byte("unknown: true"))
			case "missing-results":
				require.NoError(t, os.Remove(filepath.Join(dir, "results.yaml")))
			case "missing-delegation":
				require.NoError(t, os.Remove(filepath.Join(dir, "delegations", "d-b", "record.yaml")))
			case "unknown-delegation":
				publicationWrite(t, filepath.Join(dir, "delegations", "d-b", "future.yaml"), []byte("unknown: true"))
			case "missing-corrections":
				require.NoError(t, os.Remove(filepath.Join(dir, "corrections.yaml")))
			case "bad-corrections":
				publicationWrite(t, filepath.Join(dir, "corrections.yaml"), []byte("version: 2\nmission: "+publicationID+"\nentries: []\n"))
			case "null-corrections":
				publicationWrite(t, filepath.Join(dir, "corrections.yaml"), []byte("version: 1\nmission: "+publicationID+"\nentries: null\n"))
			case "missing-decisions", "unknown-contract":
				data, err := os.ReadFile(filepath.Join(dir, "contract.yaml"))
				require.NoError(t, err)
				if kind == "missing-decisions" {
					data = []byte(strings.Split(string(data), "round_decisions:")[0])
				} else {
					data = append(data, []byte("unknown: true\n")...)
				}
				publicationWrite(t, filepath.Join(dir, "contract.yaml"), data)
			case "multiple-results":
				publicationWrite(t, filepath.Join(dir, "results.yaml"), []byte("results: []\n---\nresults: []\n"))
			case "summary":
				publicationWrite(t, filepath.Join(s.policy.RecordRoot(), "missions.jsonl"), []byte("{\"id\":\""+publicationID+"\",\"status\":\"open\"}\n"))
			case "missing-summary":
				require.NoError(t, os.Remove(filepath.Join(s.policy.RecordRoot(), "missions.jsonl")))
			case "adr":
				publicationWrite(t, filepath.Join(s.policy.RecordRoot(), "adrs", "DES-001.yaml"), []byte("id: DES-999\n"))
			case "explicit-adr":
				adrs = []string{"DES-999"}
			case "symlink":
				require.NoError(t, os.Remove(filepath.Join(dir, "corrections.yaml")))
				require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), filepath.Join(dir, "corrections.yaml")))
			case "parent-symlink":
				moved := dir + "-moved"
				require.NoError(t, os.Rename(dir, moved))
				require.NoError(t, os.Symlink(moved, dir))
			case "results-directory":
				require.NoError(t, os.Remove(filepath.Join(dir, "results.yaml")))
				require.NoError(t, os.Mkdir(filepath.Join(dir, "results.yaml"), 0o700))
			}
			snapshot, err := s.SnapshotSelection(context.Background(), []string{publicationID}, adrs)
			require.Error(t, err)
			assert.Empty(t, snapshot.Records, "no incomplete-success snapshot")
		})
	}
}

func TestPublicationSnapshotWaitsForMissionWriter(t *testing.T) {
	s, _ := publicationStore(t)
	release, err := AcquireMissionLock(s.repoRoot, publicationID)
	require.NoError(t, err)
	defer release()
	done := make(chan error, 1)
	go func() { _, err := s.SnapshotSelection(context.Background(), []string{publicationID}, nil); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("snapshot crossed an active mission writer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot stayed blocked after writer released")
	}
}

func TestPublicationSnapshotWaitsForDelegationWriter(t *testing.T) {
	s, _ := publicationStore(t)
	release, err := s.AcquireDelegationLock("d-b")
	require.NoError(t, err)
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := s.SnapshotSelection(t.Context(), []string{publicationID}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("snapshot crossed an active delegation writer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot stayed blocked after delegation writer released")
	}
}

func TestPublicationSnapshotCancellationAndSelection(t *testing.T) {
	s, _ := publicationStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.SnapshotSelection(ctx, []string{publicationID}, nil)
	require.ErrorIs(t, err, context.Canceled)
	for _, ids := range [][]string{nil, {"../escape"}, {publicationID, publicationID}} {
		_, err := s.SnapshotSelection(context.Background(), ids, nil)
		require.Error(t, err)
	}
}

func TestPublicationSummaryLockCoordinatesTraceAppend(t *testing.T) {
	s, _ := publicationStore(t)
	row := []byte("{\"id\":\"m-2026-11-04-002\"}\n")
	done := make(chan error, 1)
	require.NoError(t, WithSummaryLock(s.repoRoot, func() error {
		go func() { done <- s.appendTraceLine(row) }()
		select {
		case err := <-done:
			t.Fatalf("close-time summary append crossed publication lock: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		return nil
	}))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("summary append stayed blocked after publication released")
	}
	data, err := os.ReadFile(RepoStatePath(s.repoRoot, "missions.jsonl"))
	require.NoError(t, err)
	// The sink guard decodes and canonically re-encodes a TraceSummary, adding
	// its zero-valued fields. Check the two logical rows, not the input syntax.
	rows := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	require.Len(t, rows, 2)
	for i, id := range []string{publicationID, "m-2026-11-04-002"} {
		var summary TraceSummary
		require.NoError(t, json.Unmarshal(rows[i], &summary))
		assert.Equal(t, id, summary.ID)
	}
}

func TestPublicationSnapshotDoesNotRequireResultsForCrossedRounds(t *testing.T) {
	for _, status := range []string{StatusOpen, StatusClosed} {
		t.Run(status, func(t *testing.T) {
			s, dir := publicationStore(t)
			if status == StatusOpen {
				data, err := os.ReadFile(filepath.Join(dir, "contract.yaml"))
				require.NoError(t, err)
				c, err := DecodeContractStrict(data, publicationID)
				require.NoError(t, err)
				c.Status = StatusOpen
				c.ClosedAt = ""
				publicationWrite(t, filepath.Join(dir, "contract.yaml"), publicationYAML(t, c))
				require.NoError(t, os.Remove(filepath.Join(dir, "results.yaml")))
				require.NoError(t, os.Remove(filepath.Join(s.policy.RecordRoot(), "missions.jsonl")))
			} else {
				// Advancing from round one requires reflection, not a result.
				rf := resultsFile{Results: []Result{{Mission: publicationID, Round: 2, Author: "bwk", Verdict: "pass", Confidence: 1, Evidence: []EvidenceCheck{{Name: "check", Status: "pass"}}}}}
				publicationWrite(t, filepath.Join(dir, "results.yaml"), publicationYAML(t, rf))
			}
			snapshot, err := s.SnapshotSelection(t.Context(), []string{publicationID}, nil)
			require.NoError(t, err)
			require.NoError(t, ValidatePublicationSnapshot(snapshot))
			for _, record := range snapshot.Records {
				if strings.HasSuffix(record.Path, "/results.yaml") {
					var rf resultsFile
					require.NoError(t, publicationDecode(record.Data, &rf))
					require.Len(t, rf.Results, 1)
					assert.Equal(t, 2, rf.Results[0].Round)
				}
			}
		})
	}
}

func TestPublicationSnapshotRedactsValuesAndDiscardedSyntax(t *testing.T) {
	s, dir := publicationStore(t)
	name := filepath.Join(dir, "contract.yaml")
	data, err := os.ReadFile(name)
	require.NoError(t, err)
	c, err := DecodeContractStrict(data, publicationID)
	require.NoError(t, err)
	c.SuccessCriteria = []string{"check /home/private-user/work and contact private@example.invalid"}
	data = append([]byte("# private source syntax /home/comment-user/work\n"), publicationYAML(t, c)...)
	publicationWrite(t, name, data)
	snapshot, err := s.SnapshotSelection(t.Context(), []string{publicationID}, nil)
	require.NoError(t, err)
	for _, record := range snapshot.Records {
		assert.NotContains(t, string(record.Data), "/home/private-user/")
		assert.NotContains(t, string(record.Data), "private@example.invalid")
		assert.NotContains(t, string(record.Data), "comment-user")
	}
	persisted, err := os.ReadFile(name)
	require.NoError(t, err)
	assert.Equal(t, data, persisted, "publication does not mutate the authority to redact it")
}

func TestPublicationSummaryGuardPreservesBytesAndRefusesMalformedInput(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		data string
		bad  bool
	}{
		{"empty", "", false},
		{"unknown-fields-and-formatting", "\n {\"id\":\"m-safe\", \"future\":{\"nested\":[true,12.30]}} \r\n", false},
		{"redacted-values", `{"id":"m-safe","future":["[redacted-email]","~/work","<repo>/file"]}`, false},
		{"path-only-fields", `{"id":"m-safe","write_set":["app/home/page.tsx","src/private@example.invalid.go","Bash(gh auth token:*)"],"files_changed":["app/Users/page.tsx"]}`, false},
		{"bad-json", `{"id":"m-safe","title":}`, true},
		{"trailing-json", `{"id":"m-safe"} {"id":"m-next"}`, true},
		{"duplicate-key", `{"id":"m-safe","title":"safe","title":"different"}`, true},
		{"duplicate-nested-key", `{"id":"m-safe","future":{"title":1,"title":2}}`, true},
		{"invalid-utf8", "{\"id\":\"m-safe\",\"title\":\"\xff\"}", true},
		{"lone-surrogate", `{"id":"m-safe","title":"\ud800"}`, true},
		{"foreign-home", `{"id":"m-safe","title":"/home/private-user/work"}`, true},
		{"email", `{"id":"m-safe","title":"private@example.invalid"}`, true},
		{"unknown-nested-secret", `{"id":"m-safe","future":{"password":"private-value"}}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(tt.data)
			before := bytes.Clone(data)
			err := GuardPublicationSummary(root, data)
			if tt.bad {
				require.ErrorContains(t, err, "refusing to publish missions.jsonl")
				assert.NotContains(t, err.Error(), "private-user")
				assert.NotContains(t, err.Error(), "private@example.invalid")
				assert.NotContains(t, err.Error(), "private-value")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, before, data, "the guard never rewrites summary bytes")
		})
	}
}
