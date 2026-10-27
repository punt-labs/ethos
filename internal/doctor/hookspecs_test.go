package doctor

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// TestHookSpecsMatchLayoutHookNames: the hooks doctor checks are exactly the
// hooks `ethos enable` writes and the path-class table classifies.
func TestHookSpecsMatchLayoutHookNames(t *testing.T) {
	files := make(map[string]bool)
	for _, s := range hookSpecs {
		files[s.File] = true
	}
	names := make(map[string]bool)
	for _, n := range layout.HookNames() {
		names[n] = true
		if !files[n] {
			t.Errorf("layout.HookNames has %q, doctor has no hook spec for it", n)
		}
	}
	for f := range files {
		if !names[f] {
			t.Errorf("doctor checks hook %q, which layout.HookNames does not list", f)
		}
	}
}
