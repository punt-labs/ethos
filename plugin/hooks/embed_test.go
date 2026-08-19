package hooks

import (
	"strings"
	"testing"
)

// TestEmbedsNonEmptyShellScripts guards the go:embed directives themselves:
// a build with a broken embed path compiles (the directive is checked at
// compile time) but an empty or wrong-content var would only surface at
// runtime, in whatever caller happens to read it first.
func TestEmbedsNonEmptyShellScripts(t *testing.T) {
	tests := []struct {
		name string
		src  []byte
	}{
		{"PreCommit", PreCommit},
		{"CommitMsg", CommitMsg},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.src) == 0 {
				t.Fatal("embedded script is empty")
			}
			if !strings.HasPrefix(string(tt.src), "#!") {
				n := min(len(tt.src), 20)
				t.Errorf("script does not start with a shebang: %q", tt.src[:n])
			}
		})
	}
}

// TestTagIdentConstantsMatchTheirScripts pins the relocated tag/ident
// constants (moved here from internal/enable per
// docs/design-hook-drift-detection.md) against the scripts they identify --
// the exact two-copies-drift pattern this move exists to prevent, checked at
// the source.
func TestTagIdentConstantsMatchTheirScripts(t *testing.T) {
	if SealTag == "" || SealIdent == "" || TrailerTag == "" || TrailerIdent == "" {
		t.Fatal("a hook tag/ident constant is empty")
	}
	if !strings.Contains(string(PreCommit), SealIdent) {
		t.Errorf("PreCommit does not carry SealIdent %q", SealIdent)
	}
	if !strings.Contains(string(CommitMsg), TrailerIdent) {
		t.Errorf("CommitMsg does not carry TrailerIdent %q", TrailerIdent)
	}
}
