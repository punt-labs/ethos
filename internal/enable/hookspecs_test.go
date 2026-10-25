package enable

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// TestEveryClassifiedHookHasATemplate: layout.HookNames is the list the
// path-class table reads and chainHooks writes from; the two cannot disagree.
func TestEveryClassifiedHookHasATemplate(t *testing.T) {
	names := make(map[string]bool)
	for _, n := range layout.HookNames() {
		names[n] = true
		if _, ok := hookSpecs[n]; !ok {
			t.Errorf("layout.HookNames has %q, hookSpecs has no template for it", n)
		}
	}
	for n := range hookSpecs {
		if !names[n] {
			t.Errorf("hookSpecs has %q, which layout.HookNames does not list", n)
		}
	}
}
