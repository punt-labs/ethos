//go:build !windows

package mission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

// TraceSummary is one JSONL line appended to <repoRoot>/.ethos/missions.jsonl
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
// <repoRoot>/.ethos/missions.jsonl. Returns nil when repoRoot is
// empty (not in a repo context). Errors are non-fatal: the caller
// logs them to stderr but does not fail the Close.
func (s *Store) appendTraceSummary(c *Contract, result *Result) error {
	if s.repoRoot == "" {
		return nil
	}
	dir := filepath.Join(s.repoRoot, ".ethos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	summary := buildTraceSummary(c, result)
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Flock missions.jsonl itself to serialize concurrent trace writes.
	f, err := os.OpenFile(
		filepath.Join(dir, "missions.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	_, writeErr := f.Write(data)
	return writeErr
}
