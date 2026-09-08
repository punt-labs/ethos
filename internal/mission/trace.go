package mission

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// TraceSummary is one JSONL line appended to <repoRoot>/.punt-labs/ethos/missions.jsonl
// when a mission closes. It captures the contract metadata and the
// closing result so every closed mission is visible in the repo's git
// history without reading the global missions directory.
type TraceSummary struct {
	ID              string   `json:"id"`
	CreatedAt       string   `json:"created_at"`
	ClosedAt        string   `json:"closed_at"`
	Status          string   `json:"status"`
	Type            string   `json:"type,omitempty"`
	Leader          string   `json:"leader"`
	Worker          string   `json:"worker"`
	Evaluator       string   `json:"evaluator"`
	Ticket          string   `json:"ticket,omitempty"`
	WriteSet        []string `json:"write_set"`
	SuccessCriteria []string `json:"success_criteria"`
	RoundsUsed      int      `json:"rounds_used"`
	RoundsBudgeted  int      `json:"rounds_budgeted"`
	Verdict         string   `json:"verdict"`
	FilesChanged    []string `json:"files_changed"`
	Pipeline        string   `json:"pipeline,omitempty"`
	Session         string   `json:"session,omitempty"`
	Repo            string   `json:"repo,omitempty"`
}

// buildTraceSummary maps a closed contract and its satisfying result
// into a TraceSummary for the JSONL trace log.
func buildTraceSummary(c *Contract, result *Result) TraceSummary {
	var files []string
	for _, fc := range result.FilesChanged {
		files = append(files, fc.Path)
	}
	ts := TraceSummary{
		ID:              c.MissionID,
		CreatedAt:       c.CreatedAt,
		ClosedAt:        c.ClosedAt,
		Status:          c.Status,
		Type:            c.Type,
		Leader:          c.Leader,
		Worker:          c.Worker,
		Evaluator:       c.Evaluator.Handle,
		Ticket:          c.Inputs.Ticket,
		WriteSet:        c.WriteSet,
		SuccessCriteria: c.SuccessCriteria,
		RoundsUsed:      c.CurrentRound,
		RoundsBudgeted:  c.Budget.Rounds,
		Verdict:         result.Verdict,
		FilesChanged:    files,
		Pipeline:        c.Pipeline,
		Session:         c.Session,
		Repo:            c.Repo,
	}
	if ts.WriteSet == nil {
		ts.WriteSet = []string{}
	}
	if ts.SuccessCriteria == nil {
		ts.SuccessCriteria = []string{}
	}
	if ts.FilesChanged == nil {
		ts.FilesChanged = []string{}
	}
	return ts
}

// appendTraceSummary writes a single JSONL line to
// <repoRoot>/.punt-labs/ethos/missions.jsonl. Returns nil when
// repoRoot is empty (not in a repo context). Errors are non-fatal:
// the caller logs them to stderr but does not fail the Close.
func (s *Store) appendTraceSummary(c *Contract, result *Result) error {
	if s.repoRoot == "" {
		return nil
	}
	dir := RepoStatePath(s.repoRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	summary := buildTraceSummary(c, result)
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Lock a dedicated sibling lock file, not missions.jsonl itself
	// (Bugbot finding on the Windows locking work): the append-only
	// handle needed to write missions.jsonl (O_APPEND|O_WRONLY) is opened
	// on Windows with FILE_APPEND_DATA access only — Go's syscall.Open
	// clears GENERIC_WRITE when O_APPEND is set — never GENERIC_READ or
	// GENERIC_WRITE, which is exactly what LockFileEx requires of its
	// handle. Locking that handle directly fails there. missions.jsonl.lock,
	// opened O_RDWR, sidesteps the requirement entirely and matches this
	// package's own convention elsewhere (id.go, store.go, delegation.go
	// all lock a sibling ".lock" file rather than the data file it
	// protects) — also preserves the data file's O_APPEND atomicity, which
	// switching it to O_RDWR to satisfy LockFileEx would have cost on
	// every platform, not just Windows.
	lockPath := filepath.Join(dir, "missions.jsonl.lock")
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := flock(lf, lockExclusive); err != nil {
		return err
	}
	defer func() { _ = funlock(lf) }()

	f, err := os.OpenFile(
		filepath.Join(dir, "missions.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return err
	}
	defer f.Close()

	_, writeErr := f.Write(data)
	return writeErr
}
