package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// TraceSummary is one JSONL line appended to <repoRoot>/.punt-labs/ethos/missions.jsonl
// when a mission closes. It captures the contract metadata and the
// closing result so every closed mission is visible in the repo's git
// history without reading the global missions directory.
type TraceSummary struct {
	ID              string   `json:"id" redact:"keep"`
	CreatedAt       string   `json:"created_at" redact:"keep"`
	ClosedAt        string   `json:"closed_at" redact:"keep"`
	Status          string   `json:"status" redact:"keep"`
	Type            string   `json:"type,omitempty" redact:"keep"`
	Leader          string   `json:"leader" redact:"handle"`
	Worker          string   `json:"worker" redact:"handle"`
	Evaluator       string   `json:"evaluator" redact:"handle"`
	Ticket          string   `json:"ticket,omitempty" redact:"path"`
	WriteSet        []string `json:"write_set" redact:"path"`
	SuccessCriteria []string `json:"success_criteria"`
	RoundsUsed      int      `json:"rounds_used"`
	RoundsBudgeted  int      `json:"rounds_budgeted"`
	Verdict         string   `json:"verdict" redact:"keep"`
	FilesChanged    []string `json:"files_changed" redact:"path"`
	Pipeline        string   `json:"pipeline,omitempty" redact:"keep"`
	Session         string   `json:"session,omitempty" redact:"session"`
	Repo            string   `json:"repo,omitempty" redact:"path"`
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

// prepareTraceSummary builds the missions.jsonl line for c and result:
// redacted, marshaled and checked by the sink guard. It returns nil with no
// error when repoRoot is empty (not in a repo context). Close and Abandon
// call it before they commit, so a record that cannot be written refuses the
// operation instead of being dropped after it.
func (s *Store) prepareTraceSummary(c *Contract, result *Result) ([]byte, Announcement, error) {
	if s.repoRoot == "" {
		return nil, nil, nil
	}
	summary, say, err := RedactRecordNotify(s.repoRoot, buildTraceSummary(c, result))
	if err != nil {
		return nil, nil, fmt.Errorf("trace summary for %s: %w", c.MissionID, err)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling trace summary for %s: %w", c.MissionID, err)
	}
	data = append(data, '\n')
	var gErr error
	if data, gErr = guardWrite("missions.jsonl", data); gErr != nil {
		return nil, nil, fmt.Errorf("trace summary for %s: %w", c.MissionID, gErr)
	}
	return data, say, nil
}

// appendTraceLine appends one prepared line to missions.jsonl and fsyncs it.
// It guards the bytes again: this is the sink.
func (s *Store) appendTraceLine(data []byte) error {
	if s.repoRoot == "" {
		return nil
	}
	dir := RepoStatePath(s.repoRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ioErr("creating", dir, err)
	}
	return withTraceLock(dir, func() error {
		var gErr error
		if data, gErr = guardWrite("missions.jsonl", data); gErr != nil {
			return gErr
		}
		f, err := os.OpenFile(
			filepath.Join(dir, "missions.jsonl"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY,
			0o644,
		)
		if err != nil {
			return ioErr("opening", filepath.Join(dir, "missions.jsonl"), err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return ioErr("writing", filepath.Join(dir, "missions.jsonl"), err)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return ioErr("syncing", filepath.Join(dir, "missions.jsonl"), err)
		}
		if err := f.Close(); err != nil {
			return ioErr("closing", filepath.Join(dir, "missions.jsonl"), err)
		}
		return nil
	})
}

// withTraceLock runs fn holding the exclusive lock that serializes every
// writer of the missions.jsonl in dir: the Close-time append and the
// scrub's rewrite.
func withTraceLock(dir string, fn func() error) error {
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

	return fn()
}
