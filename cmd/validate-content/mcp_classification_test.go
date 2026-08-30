package main

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/mcpclass"
)

func TestClassifyMCPTools(t *testing.T) {
	tests := []struct {
		name      string
		roleTools map[string][]string
		wantFail  bool
	}{
		{
			name: "classified tool passes",
			roleTools: map[string][]string{
				"go-specialist": {"Read", "mcp__plugin_quarry_quarry__find"},
			},
			wantFail: false,
		},
		{
			name: "in-repo write tool is classified",
			roleTools: map[string][]string{
				"go-specialist": {"mcp__plugin_ethos_self__identity"},
			},
			wantFail: false,
		},
		{
			name: "unclassified tool fails",
			roleTools: map[string][]string{
				"go-specialist": {"mcp__plugin_newtool_newserver__write_file"},
			},
			wantFail: true,
		},
		{
			name: "direct non-plugin-prefixed mcp tool fails, not skipped",
			roleTools: map[string][]string{
				"go-specialist": {"mcp__github__create_or_update_file"},
			},
			wantFail: true,
		},
		{
			name:      "no mcp tools at all passes",
			roleTools: map[string][]string{"ceo": {"Read"}},
			wantFail:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := classifyMCPTools(tt.roleTools)
			gotFail := false
			for _, r := range results {
				if !r.pass {
					gotFail = true
				}
			}
			if gotFail != tt.wantFail {
				t.Errorf("classifyMCPTools(%v) fail=%v, want %v (results: %+v)", tt.roleTools, gotFail, tt.wantFail, results)
			}
		})
	}
}

func TestClassifyMCPToolsAmbiguous(t *testing.T) {
	const suffix = "fake__ambiguous"
	mcpclass.ReadOnly[suffix] = "test fixture"
	mcpclass.WritesInRepo[suffix] = "test fixture"
	defer delete(mcpclass.ReadOnly, suffix)
	defer delete(mcpclass.WritesInRepo, suffix)

	roleTools := map[string][]string{
		"go-specialist": {"mcp__plugin_fake_fake__ambiguous"},
	}
	results := classifyMCPTools(roleTools)
	var found bool
	for _, r := range results {
		if !r.pass {
			found = true
		}
	}
	if !found {
		t.Errorf("classifyMCPTools(%v) = %+v, want a failure for a suffix classified in two maps", roleTools, results)
	}
}
