package seed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/role"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

	// Every starter role must ship with an `output_format` template so
	// the generator emits the `## Output Format` section in the agent
	// file. Parsing the deployed YAML into a real role.Role catches
	// regressions where `output_format:` appears as a comment or as
	// part of a responsibility string — a substring check would miss
	// both. The check is per-file so the failure message points at
	// the specific role that lost it.
	for _, name := range []string{
		"implementer", "test-engineer",
		"reviewer", "architect", "security-reviewer",
		"researcher",
	} {
		path := filepath.Join(dest, "roles", name+".yaml")
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr, "reading %s.yaml", name)
		var r role.Role
		require.NoError(t, yaml.Unmarshal(data, &r),
			"deployed role %q must parse", name)
		assert.NotEmpty(t, r.OutputFormat,
			"deployed role %q must have output_format after seed", name)
	}

	// Should have deployed all 10 talents
	assert.FileExists(t, filepath.Join(dest, "talents", "go.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "python.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "security.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "typescript.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "testing.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "code-review.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "devops.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "documentation.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "api-design.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "cli-design.md"))

	// Should have deployed skills
	assert.FileExists(t, filepath.Join(skills, "baseline-ops", "SKILL.md"))
	assert.FileExists(t, filepath.Join(skills, "mission", "SKILL.md"))

	// Should have deployed all 7 READMEs (sessions excluded)
	assert.FileExists(t, filepath.Join(dest, "identities", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "personalities", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "writing-styles", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "roles", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "skills", "README.md"))
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
	rolePath := filepath.Join(rolesDir, "implementer.yaml")
	data, err := os.ReadFile(rolePath)
	require.NoError(t, err)
	assert.NotEqual(t, "custom", string(data), "force should overwrite")
	assert.Contains(t, string(data), "name: implementer")

	// Force-written files must have 0644 permissions, not 0600 from CreateTemp
	info, err := os.Stat(rolePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "force-seeded file should be 0644")

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

// TestSeedMissionSkill checks that the mission skill is deployed
// to the right path with the Phase 3 schema content. The skill is
// the only user-facing surface for driving the mission primitive
// and its absence would silently degrade the leader workflow.
func TestSeedMissionSkill(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	_, err := Seed(dest, skills, false)
	require.NoError(t, err)

	skillPath := filepath.Join(skills, "mission", "SKILL.md")
	assert.FileExists(t, skillPath)

	data, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	content := string(data)

	// Structural anchors: every step plus the worked example must
	// be present. A future edit that drops a section fails here.
	assert.Contains(t, content, "## Step 1 — Resolve the worker")
	assert.Contains(t, content, "## Step 2 — Scaffold the contract YAML")
	assert.Contains(t, content, "## Step 3 — Pick the evaluator")
	assert.Contains(t, content, "## Step 4 — Create the mission")
	assert.Contains(t, content, "## Step 5 — Spawn the worker")
	assert.Contains(t, content, "## Step 6 — Track and review")
	assert.Contains(t, content, "## Worked example")

	// Phase 3 schema anchors: every required field name from
	// mission.Contract must appear so a future schema drift
	// surfaces as a test failure, not as a SKILL.md that teaches
	// the wrong shape.
	assert.Contains(t, content, "leader:")
	assert.Contains(t, content, "worker:")
	assert.Contains(t, content, "evaluator:")
	assert.Contains(t, content, "write_set:")
	assert.Contains(t, content, "success_criteria:")
	assert.Contains(t, content, "budget:")

	// context is a TOP-LEVEL field on mission.Contract, NOT a
	// subfield of Inputs. An earlier draft nested it under inputs;
	// Phase 3.1's strict YAML decode (KnownFields true) would have
	// rejected the worked example at `ethos mission create` time.
	// Assert top-level placement so future drift fails here
	// instead of at the store boundary.
	assert.Contains(t, content, "\ncontext: |",
		"worked example must have top-level `context: |`, not nested under inputs")
	assert.NotContains(t, content, "  context: |",
		"`context: |` must NOT be indented under inputs — mission.Contract has Context at the top level")

	// Command anchors: the real CLI surfaces the skill teaches.
	assert.Contains(t, content, "ethos mission create --file")
	assert.Contains(t, content, "ethos mission show")
	assert.Contains(t, content, "ethos mission log")
	assert.Contains(t, content, "ethos mission result")
	assert.Contains(t, content, "ethos mission close")

	// Background-spawn discipline: the Agent call MUST be
	// described as run_in_background.
	assert.Contains(t, content, "run_in_background")
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
