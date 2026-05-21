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
type PreToolUseResult struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// HandlePreToolUse enforces the verifier file allowlist. When
// ETHOS_VERIFIER_ALLOWLIST is set (by SubagentStart for verifier
// spawns), it checks whether the tool call targets a path inside the
// allowlist. If the env var is unset, the hook is a passthrough —
// all tool calls are allowed.
//
// The allowlist is a colon-separated list of paths. Repo-relative
// entries match as prefixes against the file path (after resolving
// it relative to the working directory). Absolute entries match as
// prefixes directly.
//
// DES-052 extends the rule for new-file creation. When
// ETHOS_VERIFIER_EXTRACT_INTO is set, a Write/Edit to a path that
// does not exist on disk and lives under any listed directory is
// allowed even though the path is outside ETHOS_VERIFIER_ALLOWLIST.
// Existing-file Write/Edit still requires the allowlist match. This
// is the new-file extraction surface — the worker can create
// internal/foo/cache.go under extract_into: [internal/foo/], but
// cannot modify internal/foo/bar.go unless it is in write_set.
//
// The stat is performed only on the path that did NOT match the
// allowlist, so the hot path (verifier touching a declared file) is
// unchanged.
//
// Only Write and Edit are checked — verifiers may read any file but
// may not write outside the allowlist. All other tools (Read, Glob,
// Grep, Bash, etc.) are allowed unconditionally.
func HandlePreToolUse(r io.Reader, w io.Writer) error {
	allowlist := os.Getenv("ETHOS_VERIFIER_ALLOWLIST")
	if allowlist == "" {
		return json.NewEncoder(w).Encode(PreToolUseResult{Decision: "allow"})
	}

	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("pre-tool-use: %w", err)
	}

	toolName, _ := input["tool_name"].(string)
	toolInput, _ := input["tool_input"].(map[string]any)

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
