package mission

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// missionIDPattern enforces the m-YYYY-MM-DD-NNN format.
var missionIDPattern = regexp.MustCompile(`^m-\d{4}-\d{2}-\d{2}-\d{3}$`)

// validStatuses lists the four allowed Status values.
var validStatuses = map[string]bool{
	StatusOpen:      true,
	StatusClosed:    true,
	StatusFailed:    true,
	StatusEscalated: true,
}

const (
	minRounds = 1
	maxRounds = 10
)

// Validate checks that a Contract is well-formed enough to persist.
// Called by Store.Create and Store.Update before writing to disk.
//
// Validation rules:
//  1. mission_id matches `^m-\d{4}-\d{2}-\d{2}-\d{3}$`
//  2. status is one of {open, closed, failed, escalated}
//  3. created_at is parseable as RFC3339
//  4. leader is non-empty
//  5. worker is non-empty
//  6. evaluator.handle is non-empty
//  7. evaluator.pinned_at is parseable as RFC3339
//  8. write_set is non-empty AND every entry: no `..`, not absolute,
//     not empty after trimming
//  9. budget.rounds is in [1, 10]
//  10. success_criteria has at least one entry
//
// Validate does NOT check that handles resolve to real identities.
// That's a runtime concern handled by 3.5 (verifier launch).
func (c *Contract) Validate() error {
	if c == nil {
		return fmt.Errorf("contract is nil")
	}

	// 1. mission_id
	if !missionIDPattern.MatchString(c.MissionID) {
		return fmt.Errorf("invalid mission_id %q: must match m-YYYY-MM-DD-NNN", c.MissionID)
	}

	// 2. status
	if !validStatuses[c.Status] {
		return fmt.Errorf("invalid status %q: must be one of open, closed, failed, escalated", c.Status)
	}

	// 3. created_at parseable as RFC3339
	if _, err := time.Parse(time.RFC3339, c.CreatedAt); err != nil {
		return fmt.Errorf("invalid created_at %q: %w", c.CreatedAt, err)
	}

	// 4. leader non-empty
	if strings.TrimSpace(c.Leader) == "" {
		return fmt.Errorf("leader is required")
	}

	// 5. worker non-empty
	if strings.TrimSpace(c.Worker) == "" {
		return fmt.Errorf("worker is required")
	}

	// 6. evaluator.handle non-empty
	if strings.TrimSpace(c.Evaluator.Handle) == "" {
		return fmt.Errorf("evaluator.handle is required")
	}

	// 7. evaluator.pinned_at parseable as RFC3339
	if _, err := time.Parse(time.RFC3339, c.Evaluator.PinnedAt); err != nil {
		return fmt.Errorf("invalid evaluator.pinned_at %q: %w", c.Evaluator.PinnedAt, err)
	}

	// 8. write_set is non-empty and every entry is well-formed
	if len(c.WriteSet) == 0 {
		return fmt.Errorf("write_set must contain at least one entry")
	}
	for i, entry := range c.WriteSet {
		if err := validateWriteSetEntry(entry); err != nil {
			return fmt.Errorf("write_set[%d]: %w", i, err)
		}
	}

	// 9. budget.rounds in [1, 10]
	if c.Budget.Rounds < minRounds || c.Budget.Rounds > maxRounds {
		return fmt.Errorf("budget.rounds %d out of range [%d, %d]", c.Budget.Rounds, minRounds, maxRounds)
	}

	// 10. success_criteria non-empty
	if len(c.SuccessCriteria) == 0 {
		return fmt.Errorf("success_criteria must contain at least one entry")
	}

	return nil
}

// validateWriteSetEntry rejects empty paths, absolute paths, and any
// path containing a `..` segment (path traversal).
//
// We deliberately do NOT call filepath.Clean before scanning. Clean
// would silently fold `internal/../../tmp` to `../../tmp`, which still
// flags the traversal but masks the original intent in the error
// message. Rejecting the raw form gives cleaner diagnostics for the
// reviewer auditing a malformed contract.
func validateWriteSetEntry(entry string) error {
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return fmt.Errorf("write_set entry cannot be empty or whitespace")
	}

	// Reject null bytes: any path containing \x00 is almost certainly a
	// smuggled C-string truncation attempt ("allowed/prefix\x00../etc").
	if strings.ContainsRune(trimmed, 0) {
		return fmt.Errorf("write_set entry %q contains null byte", trimmed)
	}

	// Reject absolute paths (Unix `/` and Windows-style `C:\`).
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") {
		return fmt.Errorf("write_set entry %q must be a relative path", trimmed)
	}

	// Scan every segment for literal `..`. This catches both leading
	// (`../etc/passwd`) and embedded (`internal/../../tmp`) traversal.
	// Replace any backslashes with forward slashes first so a Windows-form
	// path like `internal\..\..\tmp` does not slip through on Unix where
	// filepath.ToSlash is a no-op.
	normalized := strings.ReplaceAll(trimmed, `\`, "/")
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".." {
			return fmt.Errorf("write_set entry %q contains path traversal", trimmed)
		}
	}

	return nil
}
