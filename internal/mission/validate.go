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
// Called by Store.Create and Store.Update before writing to disk,
// and defensively on every read (Load, loadLocked).
//
// Validation rules:
//  1. mission_id matches `^m-\d{4}-\d{2}-\d{2}-\d{3}$`
//  2. status is one of {open, closed, failed, escalated}
//  3. created_at is parseable as RFC3339
//  4. updated_at is parseable as RFC3339
//  5. status ↔ closed_at invariant:
//     - status == open  → closed_at must be empty
//     - status != open  → closed_at must be non-empty and RFC3339
//  6. leader is non-empty and contains no control characters
//  7. worker is non-empty and contains no control characters
//  8. evaluator.handle is non-empty and contains no control characters
//  9. evaluator.pinned_at is parseable as RFC3339
//  10. write_set is non-empty AND every entry: no null byte, no other
//      control character, no `..` segment, not absolute (including
//      Windows drive letters and UNC), not empty after trimming
//  11. budget.rounds is in [1, 10]
//  12. success_criteria has at least one entry
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

	// 3a. updated_at parseable as RFC3339 (required field per the
	// store's Create flow, which defaults it to created_at).
	if _, err := time.Parse(time.RFC3339, c.UpdatedAt); err != nil {
		return fmt.Errorf("invalid updated_at %q: %w", c.UpdatedAt, err)
	}

	// 3b. status↔closed_at invariant:
	//   - status == open  → closed_at must be empty
	//   - status != open  → closed_at must be non-empty and RFC3339
	// The on-disk trust boundary must reject contracts that claim to
	// be open but have a closed_at timestamp, or that claim a terminal
	// status but never recorded the close time.
	if c.Status == StatusOpen {
		if c.ClosedAt != "" {
			return fmt.Errorf("status is open but closed_at is set to %q", c.ClosedAt)
		}
	} else {
		if c.ClosedAt == "" {
			return fmt.Errorf("status is %q but closed_at is empty", c.Status)
		}
		if _, err := time.Parse(time.RFC3339, c.ClosedAt); err != nil {
			return fmt.Errorf("invalid closed_at %q: %w", c.ClosedAt, err)
		}
	}

	// 4. leader non-empty and clean
	if strings.TrimSpace(c.Leader) == "" {
		return fmt.Errorf("leader is required")
	}
	if containsControlChar(c.Leader) {
		return fmt.Errorf("leader contains control character")
	}

	// 5. worker non-empty and clean
	if strings.TrimSpace(c.Worker) == "" {
		return fmt.Errorf("worker is required")
	}
	if containsControlChar(c.Worker) {
		return fmt.Errorf("worker contains control character")
	}

	// 6. evaluator.handle non-empty and clean
	if strings.TrimSpace(c.Evaluator.Handle) == "" {
		return fmt.Errorf("evaluator.handle is required")
	}
	if containsControlChar(c.Evaluator.Handle) {
		return fmt.Errorf("evaluator.handle contains control character")
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

// containsControlChar reports whether s contains any byte in the C0
// control range (0x00-0x1F) or DEL (0x7F). These bytes have no
// legitimate place in handles, paths, or the append-only log — a
// leader value containing a newline could break the JSONL log's
// one-line-per-event invariant by forging a fake event.
func containsControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
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

	// Reject null bytes first with a specific message: any path
	// containing \x00 is almost certainly a smuggled C-string
	// truncation attempt ("allowed/prefix\x00../etc"). The general
	// control-character check below would also catch this; keeping
	// the special case gives the operator a clearer diagnostic.
	if strings.ContainsRune(trimmed, 0) {
		return fmt.Errorf("write_set entry %q contains null byte", trimmed)
	}

	// Reject any other control character (newline, CR, ESC, tab, etc.).
	// A path with a newline would break the append-only log invariant
	// when an event is written that references it.
	if containsControlChar(trimmed) {
		return fmt.Errorf("write_set entry %q contains control character", trimmed)
	}

	// Normalize backslashes to forward slashes first. This makes
	// Windows-form paths evaluate the same as Unix-form paths and
	// catches UNC paths (`\\server\share` becomes `//server/share`)
	// with the same absolute-path check below.
	normalized := strings.ReplaceAll(trimmed, `\`, "/")

	// Reject absolute paths in every form we can recognize:
	//   - Unix root:   `/etc/passwd`
	//   - UNC (post-normalize): `//server/share`
	//   - Platform-specific forms via filepath.IsAbs
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(trimmed) {
		return fmt.Errorf("write_set entry %q must be a relative path", trimmed)
	}

	// Reject Windows drive-letter prefixes (`C:\foo`, `D:/bar`).
	// Neither filepath.IsAbs nor the slash check catches these on Unix,
	// so a future base-dir join could be bypassed.
	if len(trimmed) >= 2 && trimmed[1] == ':' &&
		((trimmed[0] >= 'A' && trimmed[0] <= 'Z') || (trimmed[0] >= 'a' && trimmed[0] <= 'z')) {
		return fmt.Errorf("write_set entry %q must be a relative path (drive letter)", trimmed)
	}

	// Scan every segment for literal `..`. This catches both leading
	// (`../etc/passwd`) and embedded (`internal/../../tmp`) traversal.
	// Uses the already-normalized form so `internal\..\..\tmp` is
	// caught on Unix where filepath.ToSlash is a no-op.
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".." {
			return fmt.Errorf("write_set entry %q contains path traversal", trimmed)
		}
	}

	return nil
}
