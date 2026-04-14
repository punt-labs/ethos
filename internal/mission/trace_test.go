//go:build !windows

package mission

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildTraceSummary(t *testing.T) {
	c := &Contract{
		MissionID:       "m-2026-01-01-001",
		CreatedAt:       "2026-01-01T00:00:00Z",
		ClosedAt:        "2026-01-02T00:00:00Z",
		Status:          StatusClosed,
		Type:            "implement",
		Leader:          "alice",
		Worker:          "bob",
		Evaluator:       Evaluator{Handle: "carol"},
		Inputs:          Inputs{Ticket: "T-42"},
		WriteSet:        []string{"cmd/main.go"},
		SuccessCriteria: []string{"tests pass"},
		CurrentRound:    1,
		Budget:          Budget{Rounds: 2},
		Pipeline:        "pipe-1",
		Session:         "sess-001",
		Repo:            "punt-labs/ethos",
	}
	r := &Result{
		Verdict: VerdictPass,
		FilesChanged: []FileChange{
			{Path: "cmd/main.go", Added: 10, Removed: 2},
		},
	}

	ts := buildTraceSummary(c, r)

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"ID", ts.ID, "m-2026-01-01-001"},
		{"CreatedAt", ts.CreatedAt, "2026-01-01T00:00:00Z"},
		{"ClosedAt", ts.ClosedAt, "2026-01-02T00:00:00Z"},
		{"Status", ts.Status, "closed"},
		{"Type", ts.Type, "implement"},
		{"Leader", ts.Leader, "alice"},
		{"Worker", ts.Worker, "bob"},
		{"Evaluator", ts.Evaluator, "carol"},
		{"Ticket", ts.Ticket, "T-42"},
		{"RoundsUsed", ts.RoundsUsed, 1},
		{"RoundsBudgeted", ts.RoundsBudgeted, 2},
		{"Verdict", ts.Verdict, "pass"},
		{"Pipeline", ts.Pipeline, "pipe-1"},
		{"Session", ts.Session, "sess-001"},
		{"Repo", ts.Repo, "punt-labs/ethos"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %v, want %v", tc.got, tc.want)
			}
		})
	}

	if len(ts.WriteSet) != 1 || ts.WriteSet[0] != "cmd/main.go" {
		t.Errorf("WriteSet = %v, want [cmd/main.go]", ts.WriteSet)
	}
	if len(ts.SuccessCriteria) != 1 || ts.SuccessCriteria[0] != "tests pass" {
		t.Errorf("SuccessCriteria = %v, want [tests pass]", ts.SuccessCriteria)
	}
	if len(ts.FilesChanged) != 1 || ts.FilesChanged[0] != "cmd/main.go" {
		t.Errorf("FilesChanged = %v, want [cmd/main.go]", ts.FilesChanged)
	}
}

func TestBuildTraceSummary_NoFilesChanged(t *testing.T) {
	c := &Contract{
		MissionID: "m-2026-01-01-002",
		Leader:    "alice",
		Worker:    "bob",
		Evaluator: Evaluator{Handle: "carol"},
	}
	r := &Result{Verdict: VerdictPass}

	ts := buildTraceSummary(c, r)
	if ts.FilesChanged != nil {
		t.Errorf("FilesChanged = %v, want nil", ts.FilesChanged)
	}
}

func TestAppendTraceSummary(t *testing.T) {
	dir := t.TempDir()
	s := &Store{repoRoot: dir}

	c := &Contract{
		MissionID:       "m-2026-01-01-001",
		CreatedAt:       "2026-01-01T00:00:00Z",
		ClosedAt:        "2026-01-02T00:00:00Z",
		Status:          StatusClosed,
		Leader:          "alice",
		Worker:          "bob",
		Evaluator:       Evaluator{Handle: "carol"},
		WriteSet:        []string{"a.go"},
		SuccessCriteria: []string{"ok"},
		Budget:          Budget{Rounds: 1},
		CurrentRound:    1,
	}
	r := &Result{Verdict: VerdictPass}

	// First append.
	if err := s.appendTraceSummary(c, r); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Second append (different mission).
	c2 := *c
	c2.MissionID = "m-2026-01-01-002"
	if err := s.appendTraceSummary(&c2, r); err != nil {
		t.Fatalf("second append: %v", err)
	}

	// Verify two lines of valid JSON.
	path := filepath.Join(dir, ".ethos", "missions.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var lines []TraceSummary
	for scanner.Scan() {
		var ts TraceSummary
		if err := json.Unmarshal(scanner.Bytes(), &ts); err != nil {
			t.Fatalf("invalid JSON line: %v", err)
		}
		lines = append(lines, ts)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].ID != "m-2026-01-01-001" {
		t.Errorf("line 0 ID = %q, want m-2026-01-01-001", lines[0].ID)
	}
	if lines[1].ID != "m-2026-01-01-002" {
		t.Errorf("line 1 ID = %q, want m-2026-01-01-002", lines[1].ID)
	}
}

func TestAppendTraceSummary_NoRepoRoot(t *testing.T) {
	s := &Store{repoRoot: ""}
	c := &Contract{MissionID: "m-2026-01-01-001"}
	r := &Result{Verdict: VerdictPass}

	if err := s.appendTraceSummary(c, r); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAppendTraceSummary_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	ethosDir := filepath.Join(dir, ".ethos")

	// Confirm .ethos does not exist yet.
	if _, err := os.Stat(ethosDir); !os.IsNotExist(err) {
		t.Fatalf(".ethos should not exist yet")
	}

	s := &Store{repoRoot: dir}
	c := &Contract{
		MissionID:       "m-2026-01-01-001",
		Status:          StatusClosed,
		Leader:          "alice",
		Worker:          "bob",
		Evaluator:       Evaluator{Handle: "carol"},
		WriteSet:        []string{"a.go"},
		SuccessCriteria: []string{"ok"},
		Budget:          Budget{Rounds: 1},
		CurrentRound:    1,
	}
	r := &Result{Verdict: VerdictPass}

	if err := s.appendTraceSummary(c, r); err != nil {
		t.Fatalf("append: %v", err)
	}

	info, err := os.Stat(ethosDir)
	if err != nil {
		t.Fatalf(".ethos dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf(".ethos is not a directory")
	}

	// Verify the file exists and contains valid JSON.
	path := filepath.Join(ethosDir, "missions.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var ts TraceSummary
	if err := json.Unmarshal(data, &ts); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if ts.ID != "m-2026-01-01-001" {
		t.Errorf("ID = %q, want m-2026-01-01-001", ts.ID)
	}
}
