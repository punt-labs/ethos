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
// Validation rules (14 total — must match the numbered list below
// exactly; keep the count updated when rules are added or removed):
//  1. mission_id matches `^m-\d{4}-\d{2}-\d{2}-\d{3}$`
//  2. status is one of {open, closed, failed, escalated}
//  3. created_at is parseable as RFC3339
//  4. updated_at is parseable as RFC3339
//  5. status ↔ closed_at invariant:
//     - status == open  → closed_at must be empty
//     - status != open  → closed_at must be non-empty and RFC3339
//  6. type: empty is accepted (defaulted by Store.Create / load);
//     when set, must not contain control characters
//  7. leader is non-empty and contains no control characters
//  8. worker is non-empty and contains no control characters
//  9. evaluator.handle is non-empty and contains no control characters
//  10. evaluator.pinned_at is parseable as RFC3339
//  11. write_set is non-empty AND every entry: no null byte, no other
//     control character, no `..` segment, not absolute (including
//     Windows drive letters and UNC), not empty after trimming
//  12. budget.rounds is in [1, 10]
//  13. success_criteria has at least one entry
//  14. current_round is in [1, budget.rounds] (3.4 round-tracking
//     invariant; zero is rewritten to 1 by Store.Create so a
//     pre-3.4 contract loaded in-place still parses)
//
// Validate does NOT check that handles resolve to real identities.
// That's a runtime concern handled by 3.5 (verifier launch).
func (c *Contract) Validate() error {
	if c == nil {
		return fmt.Errorf("contract is nil")
	}

	// mission_id
	if !missionIDPattern.MatchString(c.MissionID) {
		return fmt.Errorf("invalid mission_id %q: must match m-YYYY-MM-DD-NNN", c.MissionID)
	}

	// status
	if !validStatuses[c.Status] {
		return fmt.Errorf("invalid status %q: must be one of open, closed, failed, escalated", c.Status)
	}

	// created_at parseable as RFC3339
	if _, err := time.Parse(time.RFC3339, c.CreatedAt); err != nil {
		return fmt.Errorf("invalid created_at %q: %w", c.CreatedAt, err)
	}

	// updated_at parseable as RFC3339 (required field per the store's
	// Create flow, which defaults it to created_at).
	if _, err := time.Parse(time.RFC3339, c.UpdatedAt); err != nil {
		return fmt.Errorf("invalid updated_at %q: %w", c.UpdatedAt, err)
	}

	// status ↔ closed_at invariant:
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

	// type: empty is accepted (Store.Create and decodeAndValidate
	// default it to "implement"); when set, reject control characters.
	if c.Type != "" && containsControlChar(c.Type) {
		return fmt.Errorf("type contains control character")
	}

	// leader non-empty and clean
	if strings.TrimSpace(c.Leader) == "" {
		return fmt.Errorf("leader is required")
	}
	if containsControlChar(c.Leader) {
		return fmt.Errorf("leader contains control character")
	}

	// worker non-empty and clean
	if strings.TrimSpace(c.Worker) == "" {
		return fmt.Errorf("worker is required")
	}
	if containsControlChar(c.Worker) {
		return fmt.Errorf("worker contains control character")
	}

	// evaluator.handle non-empty and clean
	if strings.TrimSpace(c.Evaluator.Handle) == "" {
		return fmt.Errorf("evaluator.handle is required")
	}
	if containsControlChar(c.Evaluator.Handle) {
		return fmt.Errorf("evaluator.handle contains control character")
	}

	// evaluator.pinned_at parseable as RFC3339
	if _, err := time.Parse(time.RFC3339, c.Evaluator.PinnedAt); err != nil {
		return fmt.Errorf("invalid evaluator.pinned_at %q: %w", c.Evaluator.PinnedAt, err)
	}

	// write_set is non-empty and every entry is well-formed
	if len(c.WriteSet) == 0 {
		return fmt.Errorf("write_set must contain at least one entry")
	}
	for i, entry := range c.WriteSet {
		if err := validateWriteSetEntry(entry); err != nil {
			return fmt.Errorf("write_set[%d]: write_set entry: %w", i, err)
		}
	}

	// budget.rounds in [1, 10]
	if c.Budget.Rounds < minRounds || c.Budget.Rounds > maxRounds {
		return fmt.Errorf("budget.rounds %d out of range [%d, %d]", c.Budget.Rounds, minRounds, maxRounds)
	}

	// success_criteria non-empty
	if len(c.SuccessCriteria) == 0 {
		return fmt.Errorf("success_criteria must contain at least one entry")
	}

	// current_round in [1, budget.rounds]. Pre-3.4 contracts loaded
	// from disk may carry CurrentRound == 0; Store.Create rewrites
	// that to 1 before persisting and Store.loadLocked rewrites it
	// on read so a hand-edited or pre-3.4 file does not fail
	// validation purely because the field was added in 3.4. The
	// validation rule itself is the strict in-memory invariant: any
	// caller that has gone through Store.Create or Store.Update sees
	// 1 ≤ CurrentRound ≤ Budget.Rounds.
	if c.CurrentRound < 1 || c.CurrentRound > c.Budget.Rounds {
		return fmt.Errorf("current_round %d out of range [1, %d]", c.CurrentRound, c.Budget.Rounds)
	}

	return nil
}

// containsControlChar reports whether s contains any byte in the C0
// control range (0x00-0x1F) or DEL (0x7F). These bytes have no
// legitimate place in handles, paths, or log event fields.
//
// The append-only JSONL log encodes events via json.Marshal, which
// escapes control characters inside strings — so a leader value
// containing a newline does not literally forge a second log line.
// The real risks are: terminal/CLI injection (ANSI escape sequences
// in `ethos mission show` output, fake prompts in downstream tools),
// confusion for log readers that don't unescape JSON, and buggy
// tooling further up the stack that concatenates identity handles
// into human-readable strings without sanitization. Reject them at
// the trust boundary rather than trusting every downstream consumer
// to handle them correctly.
func containsControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// containsZeroWidth reports whether s contains any zero-width Unicode
// character. These runes are invisible in terminals and editors,
// making them a vector for path confusion and display spoofing.
func containsZeroWidth(s string) bool {
	for _, r := range s {
		switch r {
		case '\uFEFF', // BOM
			'\u200B', // ZWSP
			'\u200C', // ZWNJ
			'\u200D', // ZWJ
			'\u2060', // Word Joiner
			'\uFFFE': // noncharacter
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
//
// The error messages name only the path — callers prepend the field
// context ("write_set entry" vs "files_changed[i].path"). Phase 3.6
// round 2 moved the field prefix out so the same helper can serve
// both validators without either producing a contextually wrong
// message ("write_set entry <path>" for a files_changed violation
// was the M2 finding).
func validateWriteSetEntry(entry string) error {
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return fmt.Errorf("cannot be empty or whitespace")
	}

	// Reject null bytes first with a specific message: any path
	// containing \x00 is almost certainly a smuggled C-string
	// truncation attempt ("allowed/prefix\x00../etc"). The general
	// control-character check below would also catch this; keeping
	// the special case gives the operator a clearer diagnostic.
	if strings.ContainsRune(trimmed, 0) {
		return fmt.Errorf("%q contains null byte", trimmed)
	}

	// Reject any other control character (newline, CR, ESC, tab, etc.).
	// JSON marshaling escapes these inside strings, so a newline in a
	// write_set entry doesn't literally forge a new JSONL log line —
	// but the real risks (terminal injection via ANSI escape sequences
	// in `ethos mission show` output, log readers that don't unescape
	// JSON, downstream tooling that concatenates paths into unsanitized
	// strings) are still worth rejecting at the trust boundary. See
	// containsControlChar for the full rationale.
	if containsControlChar(trimmed) {
		return fmt.Errorf("%q contains control character", trimmed)
	}

	// Reject zero-width Unicode characters. These are invisible in most
	// terminals and editors, which makes them a vector for path confusion:
	// two paths that appear identical to the operator but differ by a
	// ZWSP or BOM would be treated as distinct by the filesystem and by
	// the conflict checker. Rejecting at the trust boundary eliminates
	// the class rather than trusting every downstream consumer.
	if containsZeroWidth(trimmed) {
		return fmt.Errorf("write_set entry %q contains zero-width Unicode character", trimmed)
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
		return fmt.Errorf("%q must be a relative path", trimmed)
	}

	// Reject Windows drive-letter prefixes (`C:\foo`, `D:/bar`).
	// Neither filepath.IsAbs nor the slash check catches these on Unix,
	// so a future base-dir join could be bypassed.
	if len(trimmed) >= 2 && trimmed[1] == ':' &&
		((trimmed[0] >= 'A' && trimmed[0] <= 'Z') || (trimmed[0] >= 'a' && trimmed[0] <= 'z')) {
		return fmt.Errorf("%q must be a relative path (drive letter)", trimmed)
	}

	// Reject root claims — entries that consist only of `.` segments
	// and slashes. The per-entry validator deliberately accepts `.`
	// segments inside a path (`./foo`, `internal/./foo`) per
	// TestValidate_AcceptsSingleDotSegment, but a STANDALONE `.` (or
	// `./`, `././.`, etc.) normalizes to "the project root" which the
	// conflict check correctly cannot represent — pathsOverlap returns
	// false against every other path because the segment list is
	// empty, so a root claim would silently coexist with every open
	// mission. Reject at the entry level so the conflict check never
	// sees an ambiguous root claim. Bugbot caught this on PR #178.
	isRootClaim := true
	for _, seg := range strings.Split(normalized, "/") {
		if seg != "" && seg != "." {
			isRootClaim = false
			break
		}
	}
	if isRootClaim {
		return fmt.Errorf("%q claims the project root via dot syntax; specify the directories or files this mission writes", trimmed)
	}

	// Scan every segment for literal `..`. This catches both leading
	// (`../etc/passwd`) and embedded (`internal/../../tmp`) traversal.
	// Uses the already-normalized form so `internal\..\..\tmp` is
	// caught on Unix where filepath.ToSlash is a no-op.
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".." {
			return fmt.Errorf("%q contains path traversal", trimmed)
		}
	}

	return nil
}
