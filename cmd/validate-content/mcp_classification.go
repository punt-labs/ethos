package main

import (
	"fmt"
	"strings"
)

// mcpToolSuffix strips the "mcp__plugin_<name>[-dev]_<server>__"
// prefix from an MCP tool name, returning the trailing
// "<server>__<method>" the classification lists key on. Both the
// released plugin name and its "-dev" counterpart collapse to the
// same suffix (DES-068's double-listing pattern, DESIGN.md:7013-7016),
// so one classification entry covers both. Returns "" for a non-MCP
// tool name. Mirrors internal/hook/pretooluse.go's mcpToolSuffix —
// duplicated here rather than imported because cmd/validate-content
// must not depend on internal/hook.
func mcpToolSuffix(toolName string) string {
	const prefix = "mcp__plugin_"
	if !strings.HasPrefix(toolName, prefix) {
		return ""
	}
	rest := toolName[len(prefix):]
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

// DES-069 R2: every mcp__ tool a role can grant must be classified
// into exactly one of the three sets below. classifyMCPTools in
// main.go fails the build when a role names a tool absent from all
// three — a grant cannot ship unclassified.
//
// Each entry cites the file:line evidence for its write behavior, in
// the tool's own repo, matching docs/mcp-write-set-gap.md's "Current
// exposure" table (DES-069).

// mcpReadOnly names tools with no write method at all.
var mcpReadOnly = map[string]string{
	// quarry: search/inspection only, no mutation.
	"quarry__find":   "quarry/src/quarry/handlers.py — search, no write",
	"quarry__show":   "quarry/src/quarry/handlers.py — read document/page",
	"quarry__status": "quarry/src/quarry/handlers.py — read daemon status",
	"quarry__list":   "quarry/src/quarry/handlers.py — list documents/collections",
	// z-spec: browse and report retrieval, no run/write.
	"zspec__browse":     "punt_zspec/commands — display only, no report write",
	"zspec__get_report": "punt_zspec/commands — reads a report already on disk",
}

// mcpWritesOutsideRepo names tools whose write method never touches a
// path under the calling repo's working tree, so it cannot violate
// the write_set invariant (which protects repo artifacts a reviewer
// diffs) no matter how it is spelled.
var mcpWritesOutsideRepo = map[string]string{
	"quarry__remember":   "quarry/src/quarry/config.py:27 — writes ~/.punt-labs/quarry/data",
	"quarry__ingest":     "quarry/src/quarry/config.py:27 — writes ~/.punt-labs/quarry/data; source is HTTP(S) only",
	"quarry__use":        "quarry/src/quarry/config.py:27 — selects daemon/session state under ~/.punt-labs/quarry/data",
	"self__session":      "internal/session/store.go:27-33 — sessionsDir() is root/sessions, and callers pass the global ~/.punt-labs/ethos root, never the repo root",
	"tty__plan":          "biff (external repo) — writes a biff-side status string, not a repo artifact",
	"tty__read_messages": "biff (external repo) — marks inbox read-state biff-side, not a repo artifact",
}

// mcpWritesInRepo names tools whose write method targets a path
// inside the calling repo's working tree. Every entry here MUST be
// matched by internal/hook/pretooluse.go's denyInRepoMCPWrite (R1) —
// this list is the source the R1 deny rule exists to cover.
var mcpWritesInRepo = map[string]string{
	"self__identity":     "internal/identity/layered.go:358-394 — Save() prefers the repo layer and writes .punt-labs/ethos/identities/<handle>.yaml, git-tracked",
	"zspec__check":       "punt_zspec/commands/check.py:10,45 + punt_zspec/report.py:44-51,77-79 — save_fuzz writes <spec>.fuzz.json beside the .tex, overwriting",
	"zspec__model_check": "punt_zspec/commands/model_check.py:11,58 + punt_zspec/report.py:44-51,77-79 — save_report writes <spec>.report.json beside the .tex, overwriting",
	"zspec__test":        "punt_zspec/commands/test.py:11,62 + punt_zspec/report.py:44-51,77-79 — save_report writes <spec>.report.json beside the .tex, overwriting",
	"zspec__animate":     "punt_zspec/commands/animate.py:11,51 + punt_zspec/report.py:44-51,77-79 — save_report writes <spec>.report.json beside the .tex, overwriting",
}

// classifyMCPTools checks every mcp__ tool name across toolLists
// (role name -> tools slice) against the three classification maps
// above. An unclassified tool is reported once per (role, tool) pair
// via the results slice, naming the tool and the role that grants it.
func classifyMCPTools(roleTools map[string][]string) []result {
	var results []result
	nChecked := 0
	nFail := 0
	for roleName, tools := range roleTools {
		for _, tool := range tools {
			suffix := mcpToolSuffix(tool)
			if suffix == "" {
				continue
			}
			nChecked++
			_, ro := mcpReadOnly[suffix]
			_, out := mcpWritesOutsideRepo[suffix]
			_, in := mcpWritesInRepo[suffix]
			if !ro && !out && !in {
				results = append(results, fail("roles: unclassified mcp tool",
					fmt.Sprintf("role %q grants %q (suffix %q) which is not in mcpReadOnly, mcpWritesOutsideRepo, or mcpWritesInRepo (cmd/validate-content/mcp_classification.go) — classify it before granting", roleName, tool, suffix)))
				nFail++
			}
		}
	}
	if nFail == 0 {
		results = append(results, pass(fmt.Sprintf("roles: mcp tool classification (%d grants checked)", nChecked)))
	}
	return results
}
