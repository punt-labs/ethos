package main

import (
	"fmt"
	"strings"

	"github.com/punt-labs/ethos/internal/mcpclass"
)

// DES-069 R2: every mcp__ tool a role can grant must be classified
// into exactly one of the three sets in internal/mcpclass —
// classifyMCPTools fails the build when a role names a tool absent
// from all three, or when a tool name carries the mcp__ prefix but
// does not parse into a classifiable suffix at all (e.g. a direct,
// non-plugin-prefixed MCP server tool such as mcp__github__*) — a
// grant cannot ship unclassified either way.
//
// internal/mcpclass carries the file:line evidence for each
// classification, matching docs/mcp-write-set-gap.md's "Current
// exposure" table (DES-069).

// classifyMCPTools checks every mcp__ tool name across toolLists
// (role name -> tools slice) against internal/mcpclass's three
// classification maps. An unclassified tool is reported once per
// (role, tool) pair via the results slice, naming the tool and the
// role that grants it.
func classifyMCPTools(roleTools map[string][]string) []result {
	var results []result
	nChecked := 0
	nFail := 0
	for roleName, tools := range roleTools {
		for _, tool := range tools {
			if !strings.HasPrefix(tool, "mcp__") {
				continue
			}
			nChecked++
			suffix := mcpclass.Suffix(tool)
			if suffix == "" {
				results = append(results, fail("roles: unclassified mcp tool",
					fmt.Sprintf("role %q grants %q, which does not parse into a mcpclass suffix (not a mcp__plugin_<name>_<server>__<tool> name) — classify it in internal/mcpclass before granting", roleName, tool)))
				nFail++
				continue
			}
			_, ro := mcpclass.ReadOnly[suffix]
			_, out := mcpclass.WritesOutsideRepo[suffix]
			_, in := mcpclass.WritesInRepo[suffix]
			if !ro && !out && !in {
				results = append(results, fail("roles: unclassified mcp tool",
					fmt.Sprintf("role %q grants %q (suffix %q) which is not in mcpclass.ReadOnly, mcpclass.WritesOutsideRepo, or mcpclass.WritesInRepo — classify it before granting", roleName, tool, suffix)))
				nFail++
			}
		}
	}
	if nFail == 0 {
		results = append(results, pass(fmt.Sprintf("roles: mcp tool classification (%d grants checked)", nChecked)))
	}
	return results
}
