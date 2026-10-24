package main

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// TestMissionExportDefaultPathsAreBlameTier pins DES-079: plain `ethos mission
// export` at its default directory writes class-2 files.
func TestMissionExportDefaultPathsAreBlameTier(t *testing.T) {
	contract, result := missionExportPaths(missionExportDefaultDir, "m-2026-10-24-001")
	for _, p := range []string{contract, result} {
		c, e, ok := layout.Classify(p)
		if !ok || c != layout.Blame {
			t.Errorf("Classify(%q) = %v, %q, %v; want blame", p, c, e.Pattern, ok)
		}
	}
}
