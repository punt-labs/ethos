package mcpclass

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"
)

// TestIdentityMethodsMatchGate pins DenyReason's hardcoded
// method=="create" gate to the actual set of methods
// internal/mcp/tools.go's handleIdentity dispatches. It parses the
// switch statement's case labels directly from source, so a new
// method added there (e.g. "update", "delete") without a matching
// change here fails this test loudly instead of silently being
// allowed by DenyReason.
//
// "create" is the only method assumed to write; every other case is
// assumed read-only. If handleIdentity ever gains a second
// write method, this test's wantMethods and the "create"-only check
// in DenyReason both need updating together.
func TestIdentityMethodsMatchGate(t *testing.T) {
	fset := token.NewFileSet()
	src := filepath.Join("..", "mcp", "tools.go")
	f, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", src, err)
	}

	var methods []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handleIdentity" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					lit, ok := expr.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					methods = append(methods, lit.Value)
				}
			}
			return false
		})
		return false
	})

	if methods == nil {
		t.Fatalf("handleIdentity not found (or has no switch) in %s — mcpclass's self__identity gate can no longer be checked against source", src)
	}
	sort.Strings(methods)

	wantMethods := []string{`"create"`, `"get"`, `"list"`, `"whoami"`}
	if len(methods) != len(wantMethods) {
		t.Fatalf("handleIdentity dispatches methods %v, want %v — update internal/mcp/tools.go's handleIdentity switch review and internal/mcpclass.DenyReason's self__identity gate together (DES-069)", methods, wantMethods)
	}
	for i, m := range methods {
		if m != wantMethods[i] {
			t.Fatalf("handleIdentity dispatches methods %v, want %v — update internal/mcp/tools.go's handleIdentity switch review and internal/mcpclass.DenyReason's self__identity gate together (DES-069)", methods, wantMethods)
		}
	}

	// The gate itself: every method discovered in the switch must be
	// denied unless it's in the known-safe read set. This asserts
	// DenyReason's behavior for each ACTUAL method, not just the
	// fixed set above — a new write method added to handleIdentity
	// without a matching DenyReason change fails here, fail-closed by
	// construction, instead of being silently allowed.
	safeReads := map[string]bool{"whoami": true, "list": true, "get": true}
	for _, quoted := range methods {
		m := quoted[1 : len(quoted)-1] // strip the surrounding quotes ast.BasicLit keeps
		_, deny := DenyReason("mcp__plugin_ethos_self__identity", map[string]any{"method": m})
		wantDeny := !safeReads[m]
		if deny != wantDeny {
			t.Errorf("DenyReason for self__identity method %q denied=%v, want %v (methods outside the known-safe read set %v must deny; update DenyReason's gate if %q is a genuine new read)", m, deny, wantDeny, safeReads, m)
		}
	}
}

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
		{"unclassified direct-server mcp tool denied fail-closed", "mcp__github__create_or_update_file", map[string]any{}, true},
		{"unparseable mcp__-prefixed tool denied fail-closed", "mcp__plugin_ethos", map[string]any{}, true},
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
