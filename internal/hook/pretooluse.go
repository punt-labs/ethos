package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
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
// Tools checked: Read, Write, Edit (file_path), Glob and Grep (path).
// All other tools are allowed unconditionally.
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

	reason := fmt.Sprintf("path %q is outside the verifier file allowlist", target)
	return json.NewEncoder(w).Encode(PreToolUseResult{
		Decision: "block",
		Reason:   reason,
	})
}

// extractTargetPath returns the file or directory path a tool call
// targets. Returns "" for tools that don't operate on file paths.
func extractTargetPath(toolName string, toolInput map[string]any) string {
	if toolInput == nil {
		return ""
	}
	switch toolName {
	case "Read", "Write", "Edit":
		p, _ := toolInput["file_path"].(string)
		return p
	case "Glob", "Grep":
		p, _ := toolInput["path"].(string)
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
// Paths are normalized with mission.CanonicalPath so "./a" and "a"
// compare equal, and the prefix check uses a "/" boundary to prevent
// "internal/hook" from matching "internal/hookextra/file.go".
func pathAllowed(target string, entries []string) bool {
	ct := mission.CanonicalPath(target)
	if ct == "" {
		return false
	}
	for _, entry := range entries {
		ce := mission.CanonicalPath(entry)
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
