package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// EvaluatePreconditions runs the admission gates on a contract's
// Preconditions list against the session's audit log. It returns the
// block reason and a deny flag plus a non-nil error when the
// predicate could not be evaluated (malformed substitution, missing
// input, unreadable audit log).
//
// Three return shapes:
//   - ("", false, nil)            — all predicates satisfied, allow
//   - (reason, true, nil)         — violated predicate, deny + reason
//   - (reason, true, err)         — unevaluable predicate, deny + reason + err
//
// The caller (HandlePreToolUse) consults
// contract.EffectiveStrictPreconditions to decide whether an
// unevaluable predicate (third shape) blocks or warns-and-allows.
//
// Empty / nil contract OR empty Preconditions short-circuit to
// allow — a contract that omits the field has no admission
// requirements. DES-054 v5 §"PreToolUse procedure preconditions".
//
// Two forms:
//
//   - implicit: the tool input's target file_path(s) must each have
//     been Read in this session. Paths are extracted from the common
//     keys file_path, notebook_path, and the entries in files/paths
//     arrays.
//   - explicit: each entry in p.RequireRead — after ${inputs.X}
//     substitution against contract.Inputs — must have been Read in
//     this session.
//
// Path comparison is on the cleaned form (filepath.Clean) so "./a"
// matches "a" and "a/./b" matches "a/b". Symlink resolution is NOT
// performed — the audit log records what the tool was asked to
// touch, not what the kernel resolved to.
func EvaluatePreconditions(
	contract *mission.Contract,
	toolName string,
	toolInput map[string]any,
	sessionID string,
	repoRoot string,
	storeRoot string,
) (denyReason string, deny bool, err error) {
	if contract == nil || len(contract.Preconditions) == 0 {
		return "", false, nil
	}
	reads, ignored, ambiguous, readErr := loadSessionReadsCounted(repoRoot, sessionID)
	for i, p := range contract.Preconditions {
		paths, ambiguousEntry, subErr := preconditionTargets(p, toolName, toolInput, contract, storeRoot, repoRoot)
		if subErr != nil {
			return preconditionMessage(p, i),
				true,
				fmt.Errorf("preconditions[%d]: %w", i, subErr)
		}
		if ambiguousEntry {
			return preconditionMessage(p, i) +
					" (a required file name contains a redaction placeholder and a file of that literal name exists: the requirement cannot be told from the file it expands to)",
				true, nil
		}
		if len(paths) == 0 {
			// Nothing to check for this predicate at this tool call.
			// Implicit form with a path-free tool, or explicit form
			// with an empty RequireRead — the latter is rejected by
			// Validate so the only reachable case here is implicit on
			// a path-free tool. Allow.
			continue
		}
		if readErr != nil {
			// The audit log could not be read but the predicate has
			// paths to check. Fail-closed under strict; the caller
			// decides. The reason carries the contract's Message so
			// the block decision is operator-actionable.
			return preconditionMessage(p, i),
				true,
				fmt.Errorf("preconditions[%d]: reading session audit: %w", i, readErr)
		}
		for _, path := range paths {
			other, otherErr := namesOtherUserHome(path, repoRoot)
			if otherErr != nil {
				return preconditionMessage(p, i), true, fmt.Errorf("preconditions[%d]: %w", i, otherErr)
			}
			if other {
				// The entry was redacted from another user's home, so no
				// Read on this machine can be the file it names.
				return preconditionMessage(p, i) +
						" (reads under another user's home cannot be verified: the path is not kept in a form that can be compared)",
					true, nil
			}
			if p.Form == mission.PreconditionFormImplicit {
				// A file name the audit log cannot tell from another (a
				// placeholder at a path boundary) cannot be matched with a Read.
				amb, ambErr := ambiguousPath(path, repoRoot)
				if ambErr != nil {
					return preconditionMessage(p, i), true, fmt.Errorf("preconditions[%d]: %w", i, ambErr)
				}
				if amb {
					return preconditionMessage(p, i) +
							" (the name contains a redaction placeholder, so a Read of it cannot be verified)",
						true, nil
				}
			}
			if !readsContain(reads, path, repoRoot) {
				msg := preconditionMessage(p, i)
				if ambiguous > 0 {
					msg += fmt.Sprintf(" (%d Read(s) were ignored: the file name contains a redaction placeholder)", ambiguous)
				}
				if ignored > 0 {
					msg += fmt.Sprintf(" (%d Read(s) were ignored: their audit lines predate the path format, so a ~ in them may name another user's home; Read the file again)", ignored)
				}
				return msg, true, nil
			}
		}
	}
	return "", false, nil
}

// preconditionMessage returns the user-facing block reason for a
// failed precondition. Falls back to a generic "preconditions[i]
// failed" string when the contract author left Message empty —
// Validate rejects that case at the trust boundary, but the helper
// stays defensive so an in-memory mutation (e.g. a malformed test
// payload) does not surface as a blank reason.
func preconditionMessage(p mission.Precondition, i int) string {
	if p.Message != "" {
		return p.Message
	}
	return fmt.Sprintf("preconditions[%d] failed", i)
}

// preconditionTargets returns the path list a precondition expects to
// see in the session audit log for the current tool call.
//
//   - implicit form: extract paths from toolInput.
//   - explicit form: substitute ${inputs.X} against contract.Inputs
//     for each entry in p.RequireRead. The substituted strings are
//     returned as-is (path normalization and abs/rel symmetry happen
//     downstream in readsContain).
//
// Substitution errors (unknown input key, malformed ${...} sequence)
// surface as the second return so the caller can mark the predicate
// unevaluable and apply the strict-fail-mode policy.
func preconditionTargets(
	p mission.Precondition,
	toolName string,
	toolInput map[string]any,
	contract *mission.Contract,
	storeRoot, repoRoot string,
) (paths []string, ambiguous bool, err error) {
	switch p.Form {
	case mission.PreconditionFormImplicit:
		return extractToolInputPaths(toolName, toolInput), false, nil
	case mission.PreconditionFormExplicit:
		out := make([]string, 0, len(p.RequireRead))
		for _, entry := range p.RequireRead {
			resolved, err := substituteInputs(entry, contract)
			if err != nil {
				return nil, false, err
			}
			// A stored entry carries <repo>, ~ and -~ in place of the paths it
			// was serialized with, and <repo> there is storeRoot, the root the
			// contract was loaded from. In a linked worktree that is not the
			// checkout, whose root only the audit log's Reads are relative to.
			literal := resolved
			resolved, err = mission.ExpandPath(resolved, storeRoot)
			if err != nil {
				return nil, false, err
			}
			// A stored entry cannot be told from the file it expands to when
			// the entry was written with a placeholder at a path boundary
			// (cache/-~-project/x stands for cache/<slug>-project/x). If the
			// literal name exists in the checkout, the requirement may mean
			// that file and is ambiguous; if it does not, the expansion is meant.
			if !mission.SamePath(resolved, literal) && repoRoot != "" && !filepath.IsAbs(literal) {
				if _, statErr := os.Lstat(filepath.Join(repoRoot, literal)); statErr == nil {
					ambiguous = true
				}
			}
			out = append(out, resolved)
		}
		return out, ambiguous, nil
	default:
		return nil, false, fmt.Errorf("unknown form %q", p.Form)
	}
}

// extractToolInputPaths pulls candidate file paths from a tool call's
// input map. The key set is the same across every non-Read tool:
// file_path (Write, Edit), notebook_path (NotebookEdit), and entries
// in files / paths arrays. Glob and Grep are pure-read tools and are
// skipped upstream by HandlePreToolUse; this helper is the reverse-
// lookup table for the remaining tools.
//
// The toolName is intentionally not consulted — keying on it would
// require this helper to track every supported tool's input schema,
// which the Claude Code hook payload renders unnecessary. The key
// set is the union across tools (Bugbot LOW on PR #328 noted the
// unused parameter; left in for caller-side readability + future
// tool-specific carve-outs).
//
// Returns the empty slice when no path-shaped value is present —
// implicit on a path-free tool is a no-op precondition.
func extractToolInputPaths(_ string, toolInput map[string]any) []string {
	if toolInput == nil {
		return nil
	}
	var paths []string
	if v, ok := toolInput["file_path"].(string); ok && v != "" {
		paths = append(paths, v)
	}
	if v, ok := toolInput["notebook_path"].(string); ok && v != "" {
		paths = append(paths, v)
	}
	paths = append(paths, extractStringArray(toolInput["files"])...)
	paths = append(paths, extractStringArray(toolInput["paths"])...)
	return paths
}

// extractStringArray returns the string values from a []any. Non-
// string entries are skipped — the audit-log scan only cares about
// path-shaped values, and a numeric or bool entry under a "files"
// key is a tool quirk, not an admission concern.
func extractStringArray(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// substituteInputs resolves ${inputs.X} markers; see mission.SubstituteInputs.
func substituteInputs(s string, contract *mission.Contract) (string, error) {
	return mission.SubstituteInputs(s, contract)
}

// loadSessionReads returns the set of file paths Read during the
// session as a canonical-path set. Repo-relative and absolute paths
// both land in the set with filepath.Clean applied so the membership
// check is symmetric on either form.
//
// Returns the empty set with a nil error when the audit log does not
// yet exist (no Read has happened in this session). Returns the
// empty set with a wrapped error on any other read failure — the
// caller treats this as an unevaluable predicate.
func loadSessionReads(repoRoot, sessionID string) (map[string]struct{}, error) {
	reads, _, _, err := loadSessionReadsCounted(repoRoot, sessionID)
	return reads, err
}

// loadSessionReadsCounted is loadSessionReads that also reports how many Reads
// it ignored because their line predates the path format and holds a ~.
func loadSessionReadsCounted(repoRoot, sessionID string) (reads map[string]struct{}, ignored, ambiguous int, err error) {
	if sessionID == "" {
		return map[string]struct{}{}, 0, 0, nil
	}
	// DES-058: the precondition evaluator reads the union of sealed
	// chunks and the live tail, not a single tracked file — a Read
	// recorded this session lives in the live zone until a seal moves it
	// into a chunk.
	entries, err := readSessionAudit(repoRoot, sessionID, time.Now())
	if err != nil {
		return map[string]struct{}{}, 0, 0, fmt.Errorf("reading session audit: %w", err)
	}
	reads = make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.Tool != "Read" {
			continue
		}
		fp, _ := e.ToolInput["file_path"].(string)
		// The placeholder stands for a path the writer withheld; it names no
		// file, and a Write of a file called [redacted-path] must not match it.
		if fp == "" || fp == redactedPath {
			continue
		}
		if e.RawPlaceholder {
			// The name reads back as another file: counted, for the deny reason.
			ambiguous++
			continue
		}
		// Audit lines carry the same placeholders a stored contract entry
		// does; both sides are expanded to this machine's paths.
		if e.PathFormat < auditPathFormat && mission.HasHomePlaceholder(fp) {
			// Written before the format existed, when another user's home
			// was also a ~: it cannot be shown to be this machine's file.
			ignored++
			continue
		}
		fp, err := mission.ExpandPath(fp, repoRoot)
		if err != nil {
			return map[string]struct{}{}, 0, 0, fmt.Errorf("reading session audit: %w", err)
		}
		reads[filepath.Clean(fp)] = struct{}{}
	}
	return reads, ignored, ambiguous, nil
}

// readsContain reports whether the audit-log set carries a Read for
// the named path. Comparison is on the cleaned form so "./a/b" and
// "a/b" match. The candidate is also checked in both directions
// against the repoRoot — a relative candidate against an absolute
// recorded read, and an absolute candidate against the repo-relative
// recorded read. The audit log records whatever the tool input was,
// not a canonicalized form, so symmetry matters (Copilot on PR #328).
func readsContain(reads map[string]struct{}, path, repoRoot string) bool {
	if len(reads) == 0 {
		return false
	}
	// has is the membership test: exact, and where the path pass folds case
	// (mission.PathCaseFold) by the same rule, so a read recorded with one
	// spelling of the checkout matches a candidate with another.
	has := func(p string) bool {
		if _, ok := reads[p]; ok {
			return true
		}
		if mission.PathCaseFold {
			for r := range reads {
				// A spelling that matches only by folding counts when it is the
				// same file: a volume can be case-sensitive, and Foo is not foo.
				if mission.SamePath(r, p) && sameFile(r, p, repoRoot) {
					return true
				}
			}
		}
		return false
	}
	clean := filepath.Clean(path)
	if has(clean) {
		return true
	}
	// Relative candidate → look for an absolute match.
	if !filepath.IsAbs(clean) {
		if abs, err := filepath.Abs(clean); err == nil && has(filepath.Clean(abs)) {
			return true
		}
		// Also try the repo-root-anchored absolute form so a
		// candidate evaluated outside the repo's cwd still matches
		// a read recorded by a tool that resolved against repoRoot.
		if repoRoot != "" && has(filepath.Clean(filepath.Join(repoRoot, clean))) {
			return true
		}
	} else if repoRoot != "" {
		// Absolute candidate → look for a repo-relative match.
		if rel, ok := relUnder(repoRoot, clean); ok && has(filepath.Clean(rel)) {
			return true
		}
	}
	return false
}

// sameFile reports whether a and b, a relative one taken from root, are one
// file on disk. A path that cannot be examined is not the same file.
func sameFile(a, b, root string) bool {
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(root, p)
	}
	fa, errA := os.Stat(abs(a))
	fb, errB := os.Stat(abs(b))
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// relUnder is the path of p relative to root when p is under root, comparing
// the prefix by mission.SamePath's case rule. A root that ends in a separator
// (the file system root) is a prefix of its children without one.
func relUnder(root, p string) (string, bool) {
	root = filepath.Clean(root)
	if len(p) < len(root) || !mission.SamePath(p[:len(root)], root) {
		return "", false
	}
	rest := p[len(root):]
	sep := string(filepath.Separator)
	if strings.HasSuffix(root, sep) {
		return rest, true
	}
	if rest != "" && !strings.HasPrefix(rest, sep) {
		return "", false
	}
	return strings.TrimPrefix(rest, sep), true
}

// envRepoRoot returns the repo root the precondition evaluator
// should read audit entries from. Delegates to resolve.EnvRepoRoot
// (the canonical helper used by every DES-054 entry point) so
// ETHOS_REPO_ROOT is trimmed consistently and the precondition path
// stays in sync with audit-write / dispatch / inheritance resolution.
// Falls back to tierBRepoRoot (which adds a Getwd fallback for the
// hook process when no env override + no .git is found) only when
// EnvRepoRoot returns empty.
//
// Exposed so HandlePreToolUse can call it with the same semantics.
func envRepoRoot() string {
	if root := resolve.EnvRepoRoot(); root != "" {
		return root
	}
	return tierBRepoRoot()
}

// namesOtherUserHome reports whether path, stored or live, is under another
// user's home directory. Audit lines and stored entries hold such a path as
// a token that cannot be compared with anything; a live path is judged by
// the same pass that wrote them.
func namesOtherUserHome(path, repoRoot string) (bool, error) {
	if mission.HasRedactedUser(path) {
		return true, nil
	}
	r, err := mission.NewPathRedactor(repoRoot)
	if err != nil {
		return false, fmt.Errorf("judging a precondition path against other users' homes: %w", err)
	}
	return mission.HasRedactedUser(r.Path(path)), nil
}
