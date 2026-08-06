package main

import "testing"

func TestMcpToolSuffix(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want string
	}{
		{"released prefix", "mcp__plugin_ethos_self__identity", "self__identity"},
		{"dev prefix", "mcp__plugin_ethos-dev_self__identity", "self__identity"},
		{"hyphenated plugin name", "mcp__plugin_z-spec_zspec__check", "zspec__check"},
		{"hyphenated dev plugin name", "mcp__plugin_z-spec-dev_zspec__check", "zspec__check"},
		{"not an mcp tool", "Write", ""},
		{"malformed, no double underscore", "mcp__plugin_ethos", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcpToolSuffix(tt.tool); got != tt.want {
				t.Errorf("mcpToolSuffix(%q) = %q, want %q", tt.tool, got, tt.want)
			}
		})
	}
}

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

// TestClassificationCoversR1DenyList pins the R2/R1 correspondence
// the design requires: every entry in mcpWritesInRepo must be a tool
// family internal/hook/pretooluse.go's denyInRepoMCPWrite denies.
// Duplicated by name here (rather than imported) since
// cmd/validate-content does not depend on internal/hook; if either
// list changes without the other, this test and
// TestHandlePreToolUse_DenyInRepoMCPWrite (internal/hook) drift
// independently and each fails on its own suite.
func TestClassificationCoversR1DenyList(t *testing.T) {
	want := map[string]bool{
		"self__identity":     true,
		"zspec__check":       true,
		"zspec__model_check": true,
		"zspec__test":        true,
		"zspec__animate":     true,
	}
	if len(mcpWritesInRepo) != len(want) {
		t.Fatalf("mcpWritesInRepo has %d entries, want %d matching the R1 deny list", len(mcpWritesInRepo), len(want))
	}
	for k := range want {
		if _, ok := mcpWritesInRepo[k]; !ok {
			t.Errorf("mcpWritesInRepo missing %q, required by R1 deny list", k)
		}
	}
}
