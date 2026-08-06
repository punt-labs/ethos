package mcpclass

import "testing"

func TestSuffix(t *testing.T) {
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
		{"direct non-plugin-prefixed mcp tool", "mcp__github__create_or_update_file", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Suffix(tt.tool); got != tt.want {
				t.Errorf("Suffix(%q) = %q, want %q", tt.tool, got, tt.want)
			}
		})
	}
}

// TestDenyReasonCoversWritesInRepo pins the R1/R2 correspondence by
// construction: every entry in WritesInRepo must produce a deny from
// DenyReason (using the "create" method for self__identity, whose
// deny is method-gated; every other entry has no benign method).
// Because DenyReason reads WritesInRepo directly, this can only fail
// if a future entry's method-gating diverges from that assumption —
// it cannot fail from a stale, hand-copied list going out of sync.
func TestDenyReasonCoversWritesInRepo(t *testing.T) {
	for suffix := range WritesInRepo {
		toolName := "mcp__plugin_ethos_" + suffix
		input := map[string]any{"method": "create"}
		_, deny := DenyReason(toolName, input)
		if !deny {
			t.Errorf("DenyReason for WritesInRepo entry %q did not deny", suffix)
		}
	}
}

func TestDenyReason(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		toolInput map[string]any
		wantDeny  bool
	}{
		{"identity create denied", "mcp__plugin_ethos_self__identity", map[string]any{"method": "create"}, true},
		{"identity whoami allowed", "mcp__plugin_ethos_self__identity", map[string]any{"method": "whoami"}, false},
		{"zspec check denied", "mcp__plugin_z-spec_zspec__check", map[string]any{}, true},
		{"zspec browse allowed (read-only, not in WritesInRepo)", "mcp__plugin_z-spec_zspec__browse", map[string]any{}, false},
		{"non-mcp tool allowed", "Write", map[string]any{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, deny := DenyReason(tt.toolName, tt.toolInput)
			if deny != tt.wantDeny {
				t.Errorf("DenyReason(%q) deny = %v, want %v (reason: %q)", tt.toolName, deny, tt.wantDeny, reason)
			}
			if deny && reason == "" {
				t.Errorf("DenyReason(%q) denied with empty reason", tt.toolName)
			}
		})
	}
}
