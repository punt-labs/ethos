package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/mission"
)

// PreToolUseResult is the JSON output of the pre-tool-use hook.
// Claude Code reads the decision field to allow or block the tool call.
//
// Continue and AdditionalEnv carry DES-054 v5 dispatch state on Agent
// tool calls. Continue defaults to true on allow paths; AdditionalEnv
// is a snake_case map per the Claude Code hook protocol. Both fields
// are omitempty so the legacy allowlist-only response shape is
// unchanged for non-Agent tool calls.
type PreToolUseResult struct {
	Decision      string            `json:"decision"`
	Reason        string            `json:"reason,omitempty"`
	Continue      bool              `json:"continue,omitempty"`
	AdditionalEnv map[string]string `json:"additional_env,omitempty"`
}

// HandlePreToolUse handles Claude Code's PreToolUse hook. It serves
// two independent purposes that share a single hook invocation:
//
//  1. Tier A advice (DES-054). When the tool is `Agent` and no
//     governance context is present (no MISSION_ID via parent, no
//     PARENT_SESSION_ID, advice not silenced), emit a one-line
//     suggestion to stderr pointing operators at `ethos mission
//     dispatch`. The advice is informational — the spawn always
//     proceeds.
//
//  2. Verifier file allowlist enforcement. When
//     ETHOS_VERIFIER_ALLOWLIST is set (by SubagentStart for verifier
//     spawns), check whether the tool call targets a path inside the
//     allowlist. If the env var is unset, the allowlist branch is a
//     passthrough — all tool calls are allowed.
//
// The two concerns are orthogonal: advice fires on Agent tool calls
// (which never carry a file_path); allowlist enforcement fires on
// Write/Edit. A single payload read serves both.
//
// Allowlist details: the list is colon-separated. Repo-relative
// entries match as prefixes against the file path (after resolving
// it relative to the working directory). Absolute entries match as
// prefixes directly. Only Write and Edit are checked — verifiers may
// read any file but may not write outside the allowlist.
//
// DES-052 extends the allowlist rule for new-file creation. When
// ETHOS_VERIFIER_EXTRACT_INTO is set, a Write/Edit to a path that
// does not exist on disk and lives under any listed directory is
// allowed even though the path is outside ETHOS_VERIFIER_ALLOWLIST.
// The stat is performed only on the path that did NOT match the
// allowlist, so the hot path (verifier touching a declared file) is
// unchanged.
func HandlePreToolUse(r io.Reader, w io.Writer) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("pre-tool-use: %w", err)
	}

	toolName, _ := input["tool_name"].(string)
	toolInput, _ := input["tool_input"].(map[string]any)
	sessionID, _ := input["session_id"].(string)

	if toolName == "Agent" {
		return dispatchAgent(w, sessionID)
	}

	// DES-054 phase 3b: Tier B preconditions admission gate. Fires
	// before the verifier allowlist check so an unsatisfied
	// must-read predicate produces an actionable, contract-supplied
	// block reason rather than a generic "outside allowlist". Pure-
	// read tools (Read, Grep, Glob) are skipped — they cannot
	// violate a must-read-first contract.
	if reason, blocked := evalContractPreconditions(toolName, toolInput, sessionID); blocked {
		return json.NewEncoder(w).Encode(PreToolUseResult{
			Decision: "block",
			Reason:   reason,
		})
	}

	allowlist := os.Getenv("ETHOS_VERIFIER_ALLOWLIST")
	if allowlist == "" {
		return json.NewEncoder(w).Encode(PreToolUseResult{Decision: "allow"})
	}

	target := extractTargetPath(toolName, toolInput)
	if target == "" {
		// Tool does not target a file path — allow unconditionally.
		return json.NewEncoder(w).Encode(PreToolUseResult{Decision: "allow"})
	}

	entries := splitAllowlist(allowlist)
	if pathAllowed(target, entries) {
		return json.NewEncoder(w).Encode(PreToolUseResult{Decision: "allow"})
	}

	// DES-052 stat-then-allow: a path outside the write_set allowlist
	// may still be authorized as a new file under an extract_into
	// directory. The check is conservative — the path must NOT exist
	// on disk; once a file exists, modification falls under the
	// write_set rule and the extract_into authorization no longer
	// applies. This prevents the modify-via-extract_into attack.
	if extractInto := os.Getenv("ETHOS_VERIFIER_EXTRACT_INTO"); extractInto != "" {
		eiEntries := splitAllowlist(extractInto)
		if pathAllowed(target, eiEntries) {
			exists, statErr := targetExists(target)
			if statErr != nil {
				// Non-IsNotExist stat failure (EACCES, EIO, ELOOP,
				// broken symlink). The conservative branch falls
				// through to block; log to stderr so the verifier
				// session has an audit trail of the ambiguous stat.
				fmt.Fprintf(os.Stderr,
					"ethos: pre-tool-use: stat %s: %v\n",
					target, statErr)
			}
			if !exists {
				return json.NewEncoder(w).Encode(PreToolUseResult{Decision: "allow"})
			}
		}
	}

	reason := fmt.Sprintf("path %q is outside the verifier file allowlist", target)
	return json.NewEncoder(w).Encode(PreToolUseResult{
		Decision: "block",
		Reason:   reason,
	})
}

// evalContractPreconditions runs the Tier B preconditions gate when
// MISSION_ID is set in the environment. Returns (reason, true) when
// the spawn must be blocked and ("", false) on every allow path —
// the caller short-circuits the existing allowlist flow on the block
// case. Pure-read tools (Read, Grep, Glob) and tools without an
// active mission context are returned as ("", false) immediately.
//
// Fail policy (DES-054 v5 §"PreToolUse procedure preconditions"):
//   - violated predicate                              -> block(reason)
//   - unevaluable + strict (default)                  -> block(reason)
//   - unevaluable + strict=false (escape hatch)       -> warn + allow
//
// Every error along the load path — missing mission store, malformed
// MISSION_ID, contract Load failure — surfaces as fail-closed under
// strict (block with reason naming the failure) and as warn-and-allow
// under strict=false. Because StrictPreconditions can only be read
// from the contract itself, a Load failure cannot consult the
// pointer; the safer side is to block, so a missing contract under a
// non-empty MISSION_ID is always treated as fail-closed.
func evalContractPreconditions(toolName string, toolInput map[string]any, sessionID string) (string, bool) {
	if toolName == "Read" || toolName == "Grep" || toolName == "Glob" {
		return "", false
	}
	missionID := os.Getenv("MISSION_ID")
	if missionID == "" {
		// Tier A spawn — no contract, no preconditions.
		return "", false
	}
	store, err := tierBMissionStore()
	if err != nil {
		// Persistence layer unreachable. The contract cannot be read
		// so the strict pointer cannot be consulted; fail-closed.
		return fmt.Sprintf("ethos pre-tool-use: resolving mission store: %v", err), true
	}
	contract, err := store.Load(missionID)
	if err != nil {
		return fmt.Sprintf("ethos pre-tool-use: resolving MISSION_ID %q: %v", missionID, err), true
	}
	repoRoot := tierBRepoRoot()
	reason, deny, evalErr := EvaluatePreconditions(contract, toolName, toolInput, sessionID, repoRoot)
	if !deny {
		return "", false
	}
	if evalErr == nil {
		// Cleanly violated predicate — always block.
		return reason, true
	}
	// Unevaluable predicate. Strict (default) blocks; non-strict
	// warns to stderr and allows the spawn through.
	if contract.EffectiveStrictPreconditions() {
		return reason, true
	}
	fmt.Fprintf(os.Stderr,
		"ethos pre-tool-use: precondition %v; strict_preconditions=false, allowing\n",
		evalErr)
	return "", false
}

// targetExists reports whether target points at an existing
// filesystem entry. Returns (true, nil) for an existing entry,
// (false, nil) for a clean IsNotExist, and (true, err) for any
// other stat error — the caller falls through to the block branch
// in the ambiguous case rather than authorize a write.
func targetExists(target string) (bool, error) {
	_, err := os.Stat(target)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return true, err
}

// extractTargetPath returns the file path a tool call targets for
// allowlist enforcement. Only Write and Edit are checked — verifiers
// may read any file but may not write outside the allowlist. Returns
// "" for all other tools, which means "allow unconditionally".
func extractTargetPath(toolName string, toolInput map[string]any) string {
	if toolInput == nil {
		return ""
	}
	switch toolName {
	case "Write", "Edit":
		// Verifiers must not modify files outside the write-set.
		// Read/Glob/Grep are unrestricted — verifiers need full read
		// access to review effectively.
		p, _ := toolInput["file_path"].(string)
		return p
	default:
		return ""
	}
}

// splitAllowlist splits a colon-separated allowlist into individual
// entries, dropping empty segments.
func splitAllowlist(raw string) []string {
	parts := strings.Split(raw, ":")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pathAllowed reports whether target is inside any allowlist entry.
// Paths are normalized with filepath.Clean and mission.CanonicalPath
// so "./a" and "a" compare equal, ".." traversals are collapsed, and
// the prefix check uses a "/" boundary to prevent "internal/hook"
// from matching "internal/hookextra/file.go".
func pathAllowed(target string, entries []string) bool {
	ct := cleanCanonical(target)
	if ct == "" {
		return false
	}
	for _, entry := range entries {
		ce := cleanCanonical(entry)
		if ce == "" {
			continue
		}
		if ct == ce {
			return true
		}
		if strings.HasPrefix(ct, ce+"/") {
			return true
		}
	}
	return false
}

// cleanCanonical applies filepath.Clean then mission.CanonicalPath.
// filepath.Clean resolves ".." segments so "a/b/../../c" becomes "c",
// preventing traversal attacks that pass a prefix check before
// resolution.
func cleanCanonical(p string) string {
	return mission.CanonicalPath(filepath.Clean(p))
}

// tierAAdvice is the one-line suggestion emitted to stderr when an
// ad-hoc Agent spawn fires PreToolUse without a governance context.
// The text matches DESIGN.md §"PreToolUse-on-Agent" literally so the
// design doc and the runtime never drift.
const tierAAdvice = "ethos: ad-hoc Agent spawn (no mission contract). " +
	"Consider 'ethos mission dispatch' for governed delegation. " +
	"(set ETHOS_QUIET_ADVICE=1 to silence)"

// maybeEmitTierAAdvice writes tierAAdvice to w unless suppression
// applies. Two independent signals silence the line:
//
//   - ETHOS_QUIET_ADVICE=1 — operator opt-out for the whole session.
//   - PARENT_SESSION_ID non-empty — a nested ad-hoc spawn whose
//     outer session already saw the advice. Either signal alone
//     suffices; both are not required.
//
// Tier A is informational: the spawn proceeds regardless. The hook's
// allow/block decision is the caller's concern.
func maybeEmitTierAAdvice(w io.Writer) {
	if os.Getenv("ETHOS_QUIET_ADVICE") == "1" {
		return
	}
	if os.Getenv("PARENT_SESSION_ID") != "" {
		return
	}
	fmt.Fprintln(w, tierAAdvice)
}
