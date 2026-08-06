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
			classes := memberships(suffix)
			switch len(classes) {
			case 0:
				results = append(results, fail("roles: unclassified mcp tool",
					fmt.Sprintf("role %q grants %q (suffix %q) which is not in mcpclass.ReadOnly, mcpclass.WritesOutsideRepo, or mcpclass.WritesInRepo — classify it before granting", roleName, tool, suffix)))
				nFail++
			case 1:
				// exactly one classification — the expected case
			default:
				results = append(results, fail("roles: ambiguous mcp tool classification",
					fmt.Sprintf("tool %q (suffix %q) is classified in more than one of mcpclass.ReadOnly, mcpclass.WritesOutsideRepo, mcpclass.WritesInRepo: %s — remove it from all but one", tool, suffix, strings.Join(classes, ", "))))
				nFail++
			}
		}
	}
	if nFail == 0 {
		results = append(results, pass(fmt.Sprintf("roles: mcp tool classification (%d grants checked)", nChecked)))
	}
	return results
}

// memberships names each of mcpclass's three classification maps that
// contains suffix. A well-formed suffix belongs to exactly one; more
// than one means the maps have drifted into an ambiguous state.
func memberships(suffix string) []string {
	var classes []string
	if _, ok := mcpclass.ReadOnly[suffix]; ok {
		classes = append(classes, "ReadOnly")
	}
	if _, ok := mcpclass.WritesOutsideRepo[suffix]; ok {
		classes = append(classes, "WritesOutsideRepo")
	}
	if _, ok := mcpclass.WritesInRepo[suffix]; ok {
		classes = append(classes, "WritesInRepo")
	}
	return classes
}
