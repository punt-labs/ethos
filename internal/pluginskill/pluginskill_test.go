package pluginskill

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSkillsMatchPluginDir is the anti-drift guard: the hardcoded `skills`
// set must equal exactly the directories under plugin/skills/. If a skill is
// added to or removed from the plugin without updating the set, this fails —
// the property both the namespacing (internal/hook) and the retirement
// (internal/seed) rely on. A runtime directory read is impossible on an
// installed consumer (no plugin dir beside the binary), so the literal is the
// source of truth and this test, run from the repo, keeps it honest.
func TestSkillsMatchPluginDir(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	skillsDir := filepath.Join(wd, "..", "..", "plugin", "skills")

	entries, err := os.ReadDir(skillsDir)
	require.NoError(t, err, "plugin/skills must be readable from the repo")

	var onDisk []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// A real skill dir carries a SKILL.md; ignore anything else.
		if _, statErr := os.Stat(filepath.Join(skillsDir, e.Name(), "SKILL.md")); statErr == nil {
			onDisk = append(onDisk, e.Name())
		}
	}
	sort.Strings(onDisk)

	want := Slugs()
	sort.Strings(want)

	assert.Equal(t, onDisk, want,
		"pluginskill.skills must match plugin/skills/ exactly; update the set when a plugin skill is added or removed")
}

func TestIs(t *testing.T) {
	assert.True(t, Is("baseline-ops"))
	assert.True(t, Is("gstack-plan"))
	assert.False(t, Is("my-skill"), "a user skill is not an ethos-plugin skill")
	assert.False(t, Is("lux:scene"), "an already-namespaced foreign skill is not an ethos-plugin skill")
	assert.False(t, Is(""), "the empty slug is not a skill")
}
