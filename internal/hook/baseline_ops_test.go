package hook

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baselineRepoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

func TestBaselineOpsSkillExists(t *testing.T) {
	repoRoot := baselineRepoRoot(t)

	skillPath := filepath.Join(repoRoot, "sidecar", "skills", "baseline-ops", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	require.NoError(t, err, "baseline-ops SKILL.md should exist")

	content := string(data)

	sections := []string{
		"## Tool Usage",
		"## Verification",
		"## Scope Discipline",
		"## Commits and Git",
		"## Security",
		"## Output",
	}
	for _, s := range sections {
		assert.Contains(t, content, s, "missing section: %s", s)
	}

	assert.True(t, strings.Contains(content, "exactly one"),
		"should contain single-command Bash rule")
	assert.True(t, strings.Contains(content, "Read") && strings.Contains(content, "cat"),
		"should mention Read over cat")
}
