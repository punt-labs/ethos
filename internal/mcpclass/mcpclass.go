// Package mcpclass classifies MCP tool names into the three sets
// DES-069 requires: read-only, writes-outside-repo, and
// writes-in-repo. It is the single source of truth for both the R1
// verifier-spawn deny (internal/hook) and the R2 build-time grant
// check (cmd/validate-content) — a tool moved into WritesInRepo here
// is denied and validated from the same literal, so the two checks
// cannot drift apart.
package mcpclass

import (
	"fmt"
	"strings"
)

// Suffix strips the "mcp__plugin_<name>[-dev]_<server>__" prefix from
// an MCP tool name, returning the trailing "<server>__<method>" the
// classification maps key on. Both the released plugin name and its
// "-dev" counterpart collapse to the same suffix (DES-068's
// double-listing pattern, DESIGN.md:7013-7016), so one entry covers
// both. Returns "" for a non-MCP tool name.
func Suffix(toolName string) string {
	const prefix = "mcp__plugin_"
	if !strings.HasPrefix(toolName, prefix) {
		return ""
	}
	rest := toolName[len(prefix):]
	// rest is "<name>[-dev]_<server>__<tool>"; the suffix we want
	// starts at the last "_" before the "__" separator, i.e. the
	// server name onward. Find "__" first, then walk back to the
	// preceding "_" that separates plugin name from server name.
	sep := strings.Index(rest, "__")
	if sep < 0 {
		return ""
	}
	pluginAndServer := rest[:sep]
	underscore := strings.LastIndex(pluginAndServer, "_")
	if underscore < 0 {
		return ""
	}
	return pluginAndServer[underscore+1:] + rest[sep:]
}

// ReadOnly names tools with no write method at all.
var ReadOnly = map[string]string{
	// quarry: search/inspection only, no mutation.
	"quarry__find":   "quarry/src/quarry/handlers.py — search, no write",
	"quarry__show":   "quarry/src/quarry/handlers.py — read document/page",
	"quarry__status": "quarry/src/quarry/handlers.py — read daemon status",
	"quarry__list":   "quarry/src/quarry/handlers.py — list documents/collections",
	// z-spec: browse and report retrieval, no run/write.
	"zspec__browse":     "punt_zspec/commands — display only, no report write",
	"zspec__get_report": "punt_zspec/commands — reads a report already on disk",
}

// WritesOutsideRepo names tools whose write method never touches a
// path under the calling repo's working tree, so it cannot violate
// the write_set invariant (which protects repo artifacts a reviewer
// diffs) no matter how it is spelled.
var WritesOutsideRepo = map[string]string{
	"quarry__remember":   "quarry/src/quarry/config.py:27 — writes ~/.punt-labs/quarry/data",
	"quarry__ingest":     "quarry/src/quarry/config.py:27 — writes ~/.punt-labs/quarry/data; source is HTTP(S) only",
	"quarry__use":        "quarry/src/quarry/config.py:27 — selects daemon/session state under ~/.punt-labs/quarry/data",
	"self__session":      "internal/session/store.go:27-33 — sessionsDir() is root/sessions, and callers pass the global ~/.punt-labs/ethos root, never the repo root",
	"tty__plan":          "biff (external repo) — writes a biff-side status string, not a repo artifact",
	"tty__read_messages": "biff (external repo) — marks inbox read-state biff-side, not a repo artifact",
}

// WritesInRepo names tools whose write method targets a path inside
// the calling repo's working tree. Every entry here is denied in
// verifier spawns by DenyReason (R1) — this map is the single source
// both the runtime deny and the build-time grant check (R2) read.
var WritesInRepo = map[string]string{
	"self__identity":     "internal/identity/layered.go:358-394 — Save() prefers the repo layer and writes .punt-labs/ethos/identities/<handle>.yaml, git-tracked",
	"zspec__check":       "punt_zspec/commands/check.py:10,45 + punt_zspec/report.py:44-51,77-79 — save_fuzz writes <spec>.fuzz.json beside the .tex, overwriting",
	"zspec__model_check": "punt_zspec/commands/model_check.py:11,58 + punt_zspec/report.py:44-51,77-79 — save_report writes <spec>.report.json beside the .tex, overwriting",
	"zspec__test":        "punt_zspec/commands/test.py:11,62 + punt_zspec/report.py:44-51,77-79 — save_report writes <spec>.report.json beside the .tex, overwriting",
	"zspec__animate":     "punt_zspec/commands/animate.py:11,51 + punt_zspec/report.py:44-51,77-79 — save_report writes <spec>.report.json beside the .tex, overwriting",
}

// DenyReason reports whether toolName is one of the in-repo MCP
// write families DES-069 denies in verifier spawns. self__identity
// is only a write on method=create — see internal/mcp/tools.go's
// handleIdentity switch (case "whoami", "list", "get", "create"):
// only "create" writes a new repo file, the rest are reads. If that
// switch grows a write method (e.g. "update", "delete"), this gate
// must be updated to match or it will silently allow the new write;
// internal/hook's TestIdentityMethodsMatchGate fails loudly when the
// two drift. The zspec report writers have no benign method, so any
// call is denied. Matching keys off WritesInRepo means a tool added
// there is denied here automatically — the deny rule cannot drift
// from the classification the build check validates.
//
// A tool named "mcp__..." that fails to classify at all — not in
// ReadOnly, WritesOutsideRepo, or WritesInRepo — is denied by
// default (fail closed), matching cmd/validate-content's
// classifyMCPTools, which fails the build on the same condition. A
// direct, non-plugin-prefixed MCP tool (e.g.
// mcp__github__create_or_update_file) parses to an empty Suffix and
// must never be silently allowed just because it isn't a
// WritesInRepo key. Returns the tool name only in the reason, never
// a path.
func DenyReason(toolName string, toolInput map[string]any) (string, bool) {
	if !strings.HasPrefix(toolName, "mcp__") {
		return "", false
	}
	suffix := Suffix(toolName)
	if suffix == "self__identity" {
		method, _ := toolInput["method"].(string)
		if method != "create" {
			return "", false
		}
		return fmt.Sprintf("tool %q method %q writes inside the repo and is denied for verifier spawns (DES-069)", toolName, method), true
	}
	if _, in := WritesInRepo[suffix]; in {
		return fmt.Sprintf("tool %q writes inside the repo and is denied for verifier spawns (DES-069)", toolName), true
	}
	if _, ro := ReadOnly[suffix]; ro {
		return "", false
	}
	if _, out := WritesOutsideRepo[suffix]; out {
		return "", false
	}
	return fmt.Sprintf("tool %q has no mcpclass classification — denied for verifier spawns until classified (DES-069)", toolName), true
}
