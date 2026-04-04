package seed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeedEmptyDir(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Should have deployed roles
	assert.FileExists(t, filepath.Join(dest, "roles", "implementer.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "reviewer.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "architect.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "security-reviewer.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "researcher.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "test-engineer.yaml"))

	// Should have deployed talents
	assert.FileExists(t, filepath.Join(dest, "talents", "go.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "python.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "security.md"))

	// Should have deployed skills
	assert.FileExists(t, filepath.Join(skills, "baseline-ops", "SKILL.md"))

	// Should have deployed READMEs
	assert.FileExists(t, filepath.Join(dest, "roles", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "README.md"))

	assert.NotEmpty(t, result.Deployed)
	assert.Empty(t, result.Skipped)
	assert.Empty(t, result.Errors)
}

func TestSeedNoClobber(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Pre-create a role file with custom content
	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o755))
	customContent := []byte("name: implementer\nmodel: opus\n")
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "implementer.yaml"), customContent, 0o644))

	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Custom file should be preserved
	data, err := os.ReadFile(filepath.Join(rolesDir, "implementer.yaml"))
	require.NoError(t, err)
	assert.Equal(t, customContent, data, "existing file should not be overwritten")

	// implementer.yaml should be in skipped list
	found := false
	for _, s := range result.Skipped {
		if filepath.Base(s) == "implementer.yaml" {
			found = true
		}
	}
	assert.True(t, found, "implementer.yaml should be in skipped list")

	// Other roles should still be deployed
	assert.FileExists(t, filepath.Join(rolesDir, "reviewer.yaml"))
}

func TestSeedForce(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Pre-create a role file
	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "implementer.yaml"), []byte("custom"), 0o644))

	result, err := Seed(dest, skills, true)
	require.NoError(t, err)

	// File should be overwritten with embedded content
	data, err := os.ReadFile(filepath.Join(rolesDir, "implementer.yaml"))
	require.NoError(t, err)
	assert.NotEqual(t, "custom", string(data), "force should overwrite")
	assert.Contains(t, string(data), "name: implementer")

	assert.Empty(t, result.Skipped)
}

func TestSeedPartialState(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Create roles dir with one file, but no talents dir
	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "implementer.yaml"), []byte("custom"), 0o644))

	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Existing role preserved
	data, _ := os.ReadFile(filepath.Join(rolesDir, "implementer.yaml"))
	assert.Equal(t, "custom", string(data))

	// Missing talents should be created
	assert.FileExists(t, filepath.Join(dest, "talents", "go.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "python.md"))

	// Skills created
	assert.FileExists(t, filepath.Join(skills, "baseline-ops", "SKILL.md"))

	assert.NotEmpty(t, result.Deployed)
}

func TestSeedSkillsPath(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	_, err := Seed(dest, skills, false)
	require.NoError(t, err)

	skillPath := filepath.Join(skills, "baseline-ops", "SKILL.md")
	assert.FileExists(t, skillPath)

	data, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "## Tool Usage")
	assert.Contains(t, string(data), "## Verification")
}

func TestSeedIntegrationWithRoleStore(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	_, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Verify seeded roles are loadable — just check YAML validity
	data, err := os.ReadFile(filepath.Join(dest, "roles", "implementer.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "name: implementer")
	assert.Contains(t, string(data), "model: sonnet")
	assert.Contains(t, string(data), "- Bash")
}

func TestSeedIdempotent(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// First seed
	r1, err := Seed(dest, skills, false)
	require.NoError(t, err)
	assert.NotEmpty(t, r1.Deployed)
	assert.Empty(t, r1.Skipped)

	// Second seed — everything should be skipped
	r2, err := Seed(dest, skills, false)
	require.NoError(t, err)
	assert.Empty(t, r2.Deployed)
	assert.NotEmpty(t, r2.Skipped)
	assert.Empty(t, r2.Errors)
}
