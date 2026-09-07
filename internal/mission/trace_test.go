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
	if len(ts.FilesChanged) != 0 {
		t.Errorf("FilesChanged = %v, want empty slice", ts.FilesChanged)
	}
	if ts.FilesChanged == nil {
		t.Errorf("FilesChanged is nil, want non-nil empty slice")
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
	path := filepath.Join(dir, ".punt-labs", "ethos", "missions.jsonl")
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

// TestAppendTraceSummary_LocksSiblingFileNotDataFile pins the Bugbot
// finding on the Windows locking work: appendTraceSummary must lock a
// dedicated missions.jsonl.lock file, not the append-only missions.jsonl
// handle itself. On Windows, O_APPEND|O_WRONLY produces a handle with only
// FILE_APPEND_DATA access (Go's syscall.Open clears GENERIC_WRITE for
// O_APPEND) -- neither GENERIC_READ nor GENERIC_WRITE, which is what
// LockFileEx requires -- so locking that handle directly would fail
// there. This repo's CI is Linux-only, so the Windows failure mode itself
// cannot be exercised here; this test instead pins the structural
// invariant that IS verifiable everywhere: the lock file exists
// afterward, sibling to the data file it protects, matching this
// package's own convention (id.go, store.go, delegation.go all lock a
// separate ".lock" file rather than the data file itself).
func TestAppendTraceSummary_LocksSiblingFileNotDataFile(t *testing.T) {
	dir := t.TempDir()
	s := &Store{repoRoot: dir}
	c := &Contract{MissionID: "m-2026-01-01-001", Leader: "alice", Worker: "bob", Evaluator: Evaluator{Handle: "carol"}}
	r := &Result{Verdict: VerdictPass}

	if err := s.appendTraceSummary(c, r); err != nil {
		t.Fatalf("append: %v", err)
	}

	ethosDir := filepath.Join(dir, ".punt-labs", "ethos")
	if _, err := os.Stat(filepath.Join(ethosDir, "missions.jsonl.lock")); err != nil {
		t.Fatalf("missions.jsonl.lock must exist alongside missions.jsonl: %v", err)
	}
}

// TestAppendTraceSummary_ConcurrentWritesSerialize exercises the actual
// locking path this repo's CI CAN verify: N goroutines appending
// concurrently must all serialize through the sibling lock file rather
// than interleaving, so every line is valid, complete JSON and no
// mission ID is lost. Regression guard for the trace.go refactor that
// introduced the separate lock file — the file this test can't reach
// (the Windows access-rights bug) is a different concern from whether
// the lock still actually serializes writers, which this test covers on
// every platform this repo builds and tests on.
func TestAppendTraceSummary_ConcurrentWritesSerialize(t *testing.T) {
	dir := t.TempDir()
	s := &Store{repoRoot: dir}
	r := &Result{Verdict: VerdictPass}

	const n = 20
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			c := &Contract{
				MissionID: "m-2026-01-01-" + string(rune('a'+i)),
				Leader:    "alice", Worker: "bob", Evaluator: Evaluator{Handle: "carol"},
			}
			errCh <- s.appendTraceSummary(c, r)
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent append %d: %v", i, err)
		}
	}

	path := filepath.Join(dir, ".punt-labs", "ethos", "missions.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	seen := make(map[string]bool)
	var lines int
	for scanner.Scan() {
		var ts TraceSummary
		if err := json.Unmarshal(scanner.Bytes(), &ts); err != nil {
			t.Fatalf("interleaved/corrupt JSON line %q: %v", scanner.Text(), err)
		}
		seen[ts.ID] = true
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lines != n {
		t.Fatalf("got %d lines, want %d — a lost or merged write means the lock did not serialize", lines, n)
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct mission IDs, want %d", len(seen), n)
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
	ethosDir := filepath.Join(dir, ".punt-labs", "ethos")

	// Confirm .punt-labs/ethos does not exist yet.
	if _, err := os.Stat(ethosDir); !os.IsNotExist(err) {
		t.Fatalf(".punt-labs/ethos should not exist yet")
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
