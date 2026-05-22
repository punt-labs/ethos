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

// pipelineIDPattern enforces lowercase alphanumeric with hyphens,
// matching the slug constraints used for pipeline names.
var pipelineIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const maxPipelineIDLen = 128

// PipelineIDValid reports whether s is a valid pipeline ID: lowercase
// alphanumeric with hyphens, starting with a letter or digit, and at
// most 128 characters.
func PipelineIDValid(s string) bool {
	return len(s) <= maxPipelineIDLen && pipelineIDPattern.MatchString(s)
}

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
// Validation rules (19 total — must match the numbered list below
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
//     Windows drive letters and UNC), not empty after trimming.
//     When an Archetype with AllowEmptyWriteSet is provided via
//     ValidateWithArchetype, rule 11 permits an empty write_set.
//     Every extract_into entry runs through the same per-entry
//     check (the same helper that validates write_set entries).
//  12. budget.rounds is in [1, 10]
//  13. success_criteria has at least one entry
//  14. current_round is in [1, budget.rounds] (3.4 round-tracking
//     invariant; zero is rewritten to 1 by Store.Create so a
//     pre-3.4 contract loaded in-place still parses)
//  15. pipeline: when set, must be a valid slug (lowercase, hyphens,
//     digits), no control characters, length ≤ 128
//  16. depends_on: each entry must be a valid mission ID; self-reference
//     is rejected
//  17. extract_into entries must be directory-shaped: an entry whose
//     basename carries a code-file extension (.go, .py, .ts, .tsx,
//     .js, .md, .yaml, .yml, .json, .toml, .rs, .java, .cpp, .h, .c)
//     is rejected. List specific known new files in write_set
//     instead. DES-052.
//  18. preconditions entries: Form is one of {implicit, explicit};
//     Message is non-empty (a failed gate is never silently named);
//     when Form == explicit, RequireRead is non-empty and every entry
//     is a well-formed relative path (same per-entry rules as
//     write_set, with ${inputs.X} placeholders permitted in the path).
//     DES-054.
//  19. delegations entries: each SpawnPattern must compile as an
//     anchored regular expression (the same `^(?:...)$` form
//     MatchSpawnPattern uses at match time). Empty SpawnPattern is
//     allowed (the runtime path treats it as never-matches). A
//     malformed regex surfaces with the pattern and the Go regexp
//     parser error — admission-time so the operator sees the
//     diagnostic before any spawn fires. DES-054 phase 3.
//
// Validate does NOT check that handles resolve to real identities.
// That's a runtime concern handled by 3.5 (verifier launch).
func (c *Contract) Validate() error {
	return c.ValidateWithArchetype(nil)
}

// ValidateWithArchetype runs the same rules as Validate but adjusts
// rule 11 (write_set non-empty) based on the archetype. When a is nil,
// all rules apply unconditionally (backward compatible).
func (c *Contract) ValidateWithArchetype(a *Archetype) error {
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

	// write_set is non-empty and every entry is well-formed.
	// When the archetype sets AllowEmptyWriteSet, an empty write_set
	// is accepted (report and inbox archetypes are read-only missions).
	if len(c.WriteSet) == 0 {
		if a == nil || !a.AllowEmptyWriteSet {
			return fmt.Errorf("write_set must contain at least one entry")
		}
	}
	for i, entry := range c.WriteSet {
		if err := validateWriteSetEntry(entry); err != nil {
			return fmt.Errorf("write_set[%d]: write_set entry: %w", i, err)
		}
	}

	// extract_into entries reuse the per-entry rules (rule 11) and add
	// rule 17 — entries must be directory-shaped (no code-file
	// extension on the basename). Empty extract_into is the
	// backward-compatible default.
	for i, entry := range c.ExtractInto {
		if err := validateWriteSetEntry(entry); err != nil {
			return fmt.Errorf("extract_into[%d]: extract_into entry: %w", i, err)
		}
		if err := validateExtractIntoShape(entry); err != nil {
			return fmt.Errorf("extract_into[%d]: %w", i, err)
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

	// pipeline: when set, must be a valid slug (lowercase, hyphens,
	// digits) with no control characters and length ≤ 128.
	if c.Pipeline != "" {
		if containsControlChar(c.Pipeline) {
			return fmt.Errorf("pipeline contains control character")
		}
		if len(c.Pipeline) > maxPipelineIDLen {
			return fmt.Errorf("pipeline %q exceeds maximum length %d", c.Pipeline, maxPipelineIDLen)
		}
		if !pipelineIDPattern.MatchString(c.Pipeline) {
			return fmt.Errorf("invalid pipeline %q: must be lowercase alphanumeric with hyphens", c.Pipeline)
		}
	}

	// depends_on: each entry must be a valid mission ID; self-reference
	// is rejected.
	for i, dep := range c.DependsOn {
		if !missionIDPattern.MatchString(dep) {
			return fmt.Errorf("depends_on[%d]: invalid mission ID %q", i, dep)
		}
		if dep == c.MissionID {
			return fmt.Errorf("depends_on[%d]: self-reference %q", i, dep)
		}
	}

	// preconditions: per-entry shape check. Empty list is the
	// backward-compatible default (contract has no read-set admission
	// requirements). DES-054 v5 §"PreToolUse procedure preconditions"
	// defines two forms; Validate rejects any other value at the trust
	// boundary so the evaluator never sees an unknown form.
	for i, p := range c.Preconditions {
		if err := validatePrecondition(p); err != nil {
			return fmt.Errorf("preconditions[%d]: %w", i, err)
		}
	}

	// delegations: compile each SpawnPattern under the same anchored
	// form MatchSpawnPattern uses at match time. A malformed regex
	// surfaces here — admission time — so the operator never deploys
	// a contract whose inheritance dispatch path silently no-matches
	// at runtime. Empty pattern is allowed (matches nothing); the
	// runtime path short-circuits before regex compile in that case.
	// DES-054 phase 3.
	for i, t := range c.Delegations {
		if t.SpawnPattern == "" {
			continue
		}
		if _, err := regexp.Compile("^(?:" + t.SpawnPattern + ")$"); err != nil {
			return fmt.Errorf("delegations[%d]: invalid spawn_pattern %q: %w",
				i, t.SpawnPattern, err)
		}
	}

	return nil
}

// validatePrecondition runs rule 18 over a single Precondition. Form
// must be one of {implicit, explicit}; Message must be non-empty so a
// failed gate is never silently named; explicit form requires a
// non-empty RequireRead with every entry passing the same per-entry
// path checks as write_set. Placeholders of the form ${inputs.X} are
// permitted in the path — they are stripped before the per-entry
// helper sees the string so the dollar/brace/dot characters don't
// trip the control-char or zero-width gates.
func validatePrecondition(p Precondition) error {
	switch p.Form {
	case PreconditionFormImplicit, PreconditionFormExplicit:
		// ok
	default:
		return fmt.Errorf("invalid form %q: must be one of %q, %q",
			p.Form, PreconditionFormImplicit, PreconditionFormExplicit)
	}
	if strings.TrimSpace(p.Message) == "" {
		return fmt.Errorf("message is required")
	}
	if containsControlChar(p.Message) {
		return fmt.Errorf("message contains control character")
	}
	if p.Form == PreconditionFormExplicit {
		if len(p.RequireRead) == 0 {
			return fmt.Errorf("explicit form requires non-empty require_read")
		}
		for i, entry := range p.RequireRead {
			stripped := stripInputsPlaceholders(entry)
			if err := validateWriteSetEntry(stripped); err != nil {
				return fmt.Errorf("require_read[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// inputsPlaceholderPattern matches ${inputs.X} substitution markers in
// a precondition require_read entry. The substitution is resolved by
// the evaluator at PreToolUse time against the contract's Inputs map;
// the validator only needs to strip the marker before running the
// per-entry path checks so the dollar/brace/dot characters don't fail
// the control-char or zero-width gates.
var inputsPlaceholderPattern = regexp.MustCompile(`\$\{inputs\.[a-zA-Z0-9_]+\}`)

// stripInputsPlaceholders returns s with every ${inputs.X} substring
// replaced by a single 'x' character. The replacement preserves the
// general shape of the path so the per-entry validator still sees a
// non-empty, control-char-free relative path. The choice of 'x' is
// arbitrary; any single ASCII letter would do.
func stripInputsPlaceholders(s string) string {
	return inputsPlaceholderPattern.ReplaceAllString(s, "x")
}

// codeFileExtensions lists the file extensions a directory-shaped
// extract_into entry must not carry. The set covers Go, Python,
// TypeScript, JavaScript, Markdown, YAML/JSON/TOML configuration,
// Rust, Java, C, and C++ — the language families ethos sees across
// the punt-labs workspace. Adding a new code extension is additive
// and safe: an existing contract that listed a directory named after
// the new extension would now fail validation on next load, which
// is the intended failure mode (the entry was always shape-ambiguous).
//
// Comparison is case-insensitive — the per-entry validator already
// rejected zero-width characters and control bytes, so a lowercased
// suffix match on the basename's extension is sufficient.
var codeFileExtensions = map[string]bool{
	".go":   true,
	".py":   true,
	".ts":   true,
	".tsx":  true,
	".js":   true,
	".md":   true,
	".yaml": true,
	".yml":  true,
	".json": true,
	".toml": true,
	".rs":   true,
	".java": true,
	".cpp":  true,
	".h":    true,
	".c":    true,
}

// validateExtractIntoShape rejects entries whose basename carries a
// code-file extension. The per-entry validator (validateWriteSetEntry)
// has already accepted the entry as a well-formed relative path; this
// helper only checks the directory-shaped invariant of rule 17. The
// entry text alone decides the shape — the directory may or may not
// exist on disk at validate time (it may be created by the same change).
func validateExtractIntoShape(entry string) error {
	trimmed := strings.TrimSpace(entry)
	// Strip a single trailing slash so "docs/" and "docs" compare the
	// same — the file extension lives on the basename either way.
	// TrimSuffix (not TrimRight) keeps the trim semantics aligned with
	// enforceExtractIntoConstraints; filepath.Base strips any remaining
	// trailing slashes so a pathological "internal/foo///" still resolves
	// to basename "foo" and passes the extension check.
	normalized := strings.TrimSuffix(strings.ReplaceAll(trimmed, `\`, "/"), "/")
	if normalized == "" {
		return nil
	}
	base := filepath.Base(normalized)
	ext := strings.ToLower(filepath.Ext(base))
	if ext == "" {
		return nil
	}
	if codeFileExtensions[ext] {
		return fmt.Errorf("extract_into entry %q looks like a file (extension %q); list specific new files in write_set instead", trimmed, ext)
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

	// Reject Windows drive-letter prefixes (`C:\foo`, `D:/bar`)
	// before the general colon check so the diagnostic names the
	// real category. The colon check below catches every other
	// colon-bearing path.
	if len(trimmed) >= 2 && trimmed[1] == ':' &&
		((trimmed[0] >= 'A' && trimmed[0] <= 'Z') || (trimmed[0] >= 'a' && trimmed[0] <= 'z')) {
		return fmt.Errorf("%q must be a relative path (drive letter)", trimmed)
	}

	// Reject colons. SubagentStart joins write_set and extract_into
	// entries with `:` into ETHOS_VERIFIER_ALLOWLIST and
	// ETHOS_VERIFIER_EXTRACT_INTO; a contract entry containing a
	// colon would smuggle a second allowlist entry past admission
	// control. Reject at the trust boundary so the env-var separator
	// is unambiguous on both axes.
	if strings.ContainsRune(trimmed, ':') {
		return fmt.Errorf("%q contains colon (allowlist separator)", trimmed)
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
		return fmt.Errorf("%q contains zero-width Unicode character", trimmed)
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
	// Windows drive-letter prefixes are rejected upstream by the
	// drive-letter check; the slash check below handles the rest.
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(trimmed) {
		return fmt.Errorf("%q must be a relative path", trimmed)
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
