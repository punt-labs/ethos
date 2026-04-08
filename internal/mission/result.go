package mission

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Result is the structured worker handoff that Store.Close requires
// before a mission can transition to a terminal status.
//
// Phase 3.1 promised a typed Result artifact and deferred the schema
// to Phase 3.6. Phase 3.6 ships it: the worker writes one Result per
// round, the leader reviews it, and Store.Close refuses the terminal
// transition unless LoadResult returns a well-formed artifact for the
// mission's current round.
//
// The type is deliberately flat and strict. Every field either
// carries load-bearing data the close gate reads, or is the operator's
// audit record of what changed. Prose is optional and is never read
// by ethos; it exists only so the human reviewer has room to
// explain. The machine path takes verdict, confidence, files_changed,
// and evidence.
//
// Results are append-only: ResultStore.Append refuses to overwrite an
// existing round's result, mirroring the Phase 3.4 reflection store
// byte-for-byte.
type Result struct {
	// Mission is the full mission ID the result covers. It is
	// duplicated here (also on disk as a filename prefix) so a result
	// file cut loose from its mission can be reunited by inspection.
	// Validate rejects a file whose Mission field does not match the
	// caller's expectation — see ResultStore.Append.
	Mission string `yaml:"mission" json:"mission"`

	// Round is the round this result covers. Results are 1-indexed to
	// match Contract.CurrentRound. Append refuses any value that does
	// not equal the mission's current round at submission time.
	Round int `yaml:"round" json:"round"`

	// CreatedAt is the RFC3339 timestamp at which the worker submitted
	// the result. Set by ResultStore.Append when empty, so the caller
	// can leave it blank.
	CreatedAt string `yaml:"created_at" json:"created_at"`

	// Author is the handle of the worker that produced the result.
	// Typically equals the contract's Worker; the gate does not
	// require equality so a delegated sub-worker can still claim
	// authorship of its own output.
	Author string `yaml:"author" json:"author"`

	// Verdict is the worker's own assessment of the round. It is a
	// closed enum: pass (all success criteria met), fail (at least one
	// criterion failed), or escalate (the worker ran out of runway and
	// needs leader guidance). The close gate does not require the
	// verdict to match the terminal status — a worker may report
	// verdict=fail on a round the leader ultimately chooses to close
	// as closed_pass — but the fact that verdicts exist as a typed
	// field is what lets the leader synthesize multi-worker output.
	Verdict string `yaml:"verdict" json:"verdict"`

	// Confidence is the worker's calibrated confidence in the verdict,
	// in [0.0, 1.0]. Validate rejects values outside the closed
	// interval. A low confidence verdict=pass is a legitimate output:
	// the worker met the criteria but isn't sure the criteria
	// themselves are sufficient.
	Confidence float64 `yaml:"confidence" json:"confidence"`

	// FilesChanged is the list of files the worker modified during
	// the round. Every entry's Path must live inside the contract's
	// WriteSet (segment-prefix containment, same helper as Phase 3.2
	// write_set admission). The line counts are advisory; the gate
	// does not constrain them.
	FilesChanged []FileChange `yaml:"files_changed" json:"files_changed"`

	// Evidence is the list of named checks the worker ran and their
	// outcomes. At least one entry is required — a result with no
	// evidence is indistinguishable from "I didn't test anything",
	// which is not a reviewable artifact. Each entry's Status is a
	// closed enum.
	Evidence []EvidenceCheck `yaml:"evidence" json:"evidence"`

	// OpenQuestions captures ambiguity the worker wants the leader to
	// resolve. Optional — an unambiguous round can leave it empty.
	OpenQuestions []string `yaml:"open_questions,omitempty" json:"open_questions,omitempty"`

	// Prose is the human-facing narrative. Optional and never read by
	// ethos. Control characters and the max-length check still apply
	// so a rogue prose section cannot smuggle ANSI escapes through the
	// CLI or terminal injection through log viewers.
	Prose string `yaml:"prose,omitempty" json:"prose,omitempty"`
}

// FileChange describes a single file the worker modified during the
// round. Path is validated for containment against the contract's
// WriteSet at Append time; Added/Removed are counts with no upper
// bound but must be non-negative.
type FileChange struct {
	Path    string `yaml:"path" json:"path"`
	Added   int    `yaml:"added" json:"added"`
	Removed int    `yaml:"removed" json:"removed"`
}

// EvidenceCheck is one named check the worker ran, with its outcome.
// Name is a short operator-facing label ("go test ./internal/mission",
// "make check", "regression: append-only submission"). Status is an
// enum from a closed set — see validEvidenceStatuses.
type EvidenceCheck struct {
	Name   string `yaml:"name" json:"name"`
	Status string `yaml:"status" json:"status"`
}

// Verdict enum values. The set is closed; Validate rejects anything
// else. The names are short because they appear in gate error
// messages and in the event log.
const (
	VerdictPass     = "pass"
	VerdictFail     = "fail"
	VerdictEscalate = "escalate"
)

var validVerdicts = map[string]bool{
	VerdictPass:     true,
	VerdictFail:     true,
	VerdictEscalate: true,
}

// Evidence status enum values. Pass and fail are the common case;
// skip exists for checks the worker deliberately did not run (e.g.
// the test depends on hardware not present) — a skip still counts
// as an evidence entry so the result stays reviewable.
const (
	EvidenceStatusPass = "pass"
	EvidenceStatusFail = "fail"
	EvidenceStatusSkip = "skip"
)

var validEvidenceStatuses = map[string]bool{
	EvidenceStatusPass: true,
	EvidenceStatusFail: true,
	EvidenceStatusSkip: true,
}

// Validate checks that a Result is well-formed enough to persist.
// Called by ResultStore.Append before any disk write, and defensively
// on every read.
//
// Validation rules:
//  1. mission matches the m-YYYY-MM-DD-NNN pattern
//  2. round is in [minRounds, maxRounds]
//  3. author is non-empty and contains no control characters
//  4. verdict is one of {pass, fail, escalate}
//  5. confidence is in [0.0, 1.0]
//  6. created_at, when set, is parseable as RFC3339
//  7. files_changed may be empty only when the round made no file
//     changes; every present entry has a well-formed path (same
//     rules as write_set entries: no control characters, no absolute
//     path, no traversal, no root claim) and non-negative line counts
//  8. evidence has at least one entry; every entry has a non-empty
//     name with no control characters and a status in the closed set
//  9. open_questions entries, when present, are non-empty and free of
//     control characters
//  10. prose, when set, contains no control characters (newlines are
//      permitted inside prose; the containsControlChar helper treats
//      them as control, so prose uses a dedicated pass that allows
//      \n, \r, and \t — reviewers write multi-line prose)
//
// Validate does NOT check files_changed containment against a
// specific contract's write_set. That check is the store's job, where
// the contract is in scope. Validate enforces only the per-entry
// structural rules that are independent of the mission.
func (r *Result) Validate() error {
	if r == nil {
		return fmt.Errorf("result is nil")
	}
	if !missionIDPattern.MatchString(r.Mission) {
		return fmt.Errorf("invalid mission %q: must match m-YYYY-MM-DD-NNN", r.Mission)
	}
	if r.Round < minRounds || r.Round > maxRounds {
		return fmt.Errorf("invalid round %d: must be in [%d, %d]", r.Round, minRounds, maxRounds)
	}
	if strings.TrimSpace(r.Author) == "" {
		return fmt.Errorf("author is required")
	}
	if containsControlChar(r.Author) {
		return fmt.Errorf("author contains control character")
	}
	if !validVerdicts[r.Verdict] {
		return fmt.Errorf("invalid verdict %q: must be one of pass, fail, escalate", r.Verdict)
	}
	// NaN compares false against every bound, so a bare `< 0 || > 1`
	// check silently admits it. YAML `.nan` decodes to math.NaN(), so
	// the rejection must happen before the range check.
	if math.IsNaN(r.Confidence) {
		return fmt.Errorf("invalid confidence NaN: must be in [0.0, 1.0]")
	}
	if r.Confidence < 0.0 || r.Confidence > 1.0 {
		return fmt.Errorf("invalid confidence %v: must be in [0.0, 1.0]", r.Confidence)
	}
	if r.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, r.CreatedAt); err != nil {
			return fmt.Errorf("invalid created_at %q: %w", r.CreatedAt, err)
		}
	}
	for i, fc := range r.FilesChanged {
		if err := validateFileChange(fc); err != nil {
			return fmt.Errorf("files_changed[%d]: %w", i, err)
		}
	}
	if len(r.Evidence) == 0 {
		return fmt.Errorf("evidence must contain at least one entry")
	}
	for i, e := range r.Evidence {
		if err := validateEvidenceCheck(e); err != nil {
			return fmt.Errorf("evidence[%d]: %w", i, err)
		}
	}
	for i, q := range r.OpenQuestions {
		trimmed := strings.TrimSpace(q)
		if trimmed == "" {
			return fmt.Errorf("open_questions[%d]: entry cannot be empty or whitespace", i)
		}
		if containsControlChar(q) {
			return fmt.Errorf("open_questions[%d]: entry contains control character", i)
		}
	}
	if r.Prose != "" && containsProseControlChar(r.Prose) {
		return fmt.Errorf("prose contains control character")
	}
	return nil
}

// validateFileChange enforces the per-entry rules for a FileChange.
// Path reuses validateWriteSetEntry — the same helper that admits
// write_set entries at create time — so the two trust boundaries
// stay in lockstep. Line counts must be non-negative.
//
// Invariant: a result cannot declare a file at a path that the
// contract's write_set would itself reject. This closes the
// equivalence class enumerated in Phase 3.2 (absolute paths, drive
// letters, traversal, control characters, root claims, empty
// segments) across both admission and result submission.
//
// The field prefix is "path" rather than "write_set entry" — the
// user is editing files_changed, not write_set, and the error must
// name the field they can fix. Round 2 of Phase 3.6 moved the field
// prefix out of validateWriteSetEntry for exactly this reason.
func validateFileChange(fc FileChange) error {
	if err := validateWriteSetEntry(fc.Path); err != nil {
		return fmt.Errorf("path %w", err)
	}
	if fc.Added < 0 {
		return fmt.Errorf("added %d must be non-negative", fc.Added)
	}
	if fc.Removed < 0 {
		return fmt.Errorf("removed %d must be non-negative", fc.Removed)
	}
	return nil
}

// validateEvidenceCheck enforces the per-entry rules for an
// EvidenceCheck. Name is non-empty and free of control characters;
// status is one of the closed set.
func validateEvidenceCheck(e EvidenceCheck) error {
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("name cannot be empty or whitespace")
	}
	if containsControlChar(e.Name) {
		return fmt.Errorf("name contains control character")
	}
	if !validEvidenceStatuses[e.Status] {
		return fmt.Errorf("invalid status %q: must be one of pass, fail, skip", e.Status)
	}
	return nil
}

// containsProseControlChar is a relaxed version of containsControlChar
// for the prose field. Prose is multi-line narrative, so \n (0x0A),
// \r (0x0D), and \t (0x09) are legitimate content; every other byte
// in the C0 control range (0x00–0x1F) and DEL (0x7F) is still
// rejected so prose cannot smuggle ANSI escapes or null bytes past
// the trust boundary.
func containsProseControlChar(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// DecodeResultStrict parses a YAML result with strict rules: every
// field must be known to the Result struct, and exactly one YAML
// document must be present. Symmetric with DecodeContractStrict and
// DecodeReflectionStrict.
//
// The label argument is a human-readable identifier (file path or
// mission ID) used in error messages.
func DecodeResultStrict(data []byte, label string) (*Result, error) {
	var r Result
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("invalid result %s: %w", label, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid result %s: multiple YAML documents are not allowed", label)
		}
		return nil, fmt.Errorf("invalid result %s: trailing content after first document: %w", label, err)
	}
	return &r, nil
}
