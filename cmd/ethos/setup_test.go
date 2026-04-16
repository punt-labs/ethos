//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/seed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestEnv creates a minimal environment for setup tests: a fake
// HOME with seeded content, a git-init'd repo, and the required env vars.
// Returns the home dir and repo dir.
func setupTestEnv(t *testing.T) (home, repo string) {
	t.Helper()

	// Reset setup-specific flag state.
	setupBundle = "foundation"
	setupSolo = false
	setupFile = ""
	t.Cleanup(func() {
		setupBundle = "foundation"
		setupSolo = false
		setupFile = ""
	})

	home = t.TempDir()
	repo = t.TempDir()
	gitInitDir(t, repo, home)

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-user")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	// Run seed to deploy bundles, personalities, writing styles, etc.
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	skillsRoot := filepath.Join(home, ".claude", "skills")
	_, err := seed.Seed(globalRoot, skillsRoot, false)
	require.NoError(t, err, "seed failed")

	return home, repo
}

// writeSetupFile creates a YAML setup config at the given path.
func writeSetupFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestSetup_FileMode(t *testing.T) {
	home, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `
name: Priya Chandran
handle: priya-chandran
writing_style: concise-quantified
`)

	stdout, stderr, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err, "stderr: %s\nstdout: %s", stderr, stdout)

	// Verify human identity created.
	humanPath := filepath.Join(home, ".punt-labs", "ethos", "identities", "priya-chandran.yaml")
	assert.FileExists(t, humanPath)

	// Verify agent identity created.
	agentPath := filepath.Join(home, ".punt-labs", "ethos", "identities", "claude.yaml")
	assert.FileExists(t, agentPath)

	// Verify agent has engineering talent.
	agentID, err := identity.NewStore(filepath.Join(home, ".punt-labs", "ethos")).Load("claude", identity.Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "agent", agentID.Kind)
	assert.Contains(t, agentID.Talents, "engineering")

	// Verify repo config created.
	configPath := filepath.Join(repo, ".punt-labs", "ethos.yaml")
	assert.FileExists(t, configPath)
	configData, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(configData), "agent: claude")

	// Verify bundle activated.
	assert.Contains(t, string(configData), "active_bundle: foundation")

	// Verify agent files generated.
	agentsDir := filepath.Join(repo, ".claude", "agents")
	entries, err := os.ReadDir(agentsDir)
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "agent files should be generated")

	// Non-JSON mode prints a summary table to stdout.
	assert.Contains(t, stdout, "human identity")
	assert.Contains(t, stdout, "agent identity")
	assert.Contains(t, stdout, "bundle")
}

func TestSetup_FileMode_JSON(t *testing.T) {
	_, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `
name: Alice Test
handle: alice-test
`)

	stdout, _, err := execHandler(t, "setup", "--file", cfgPath, "--json")
	require.NoError(t, err)

	var result setupResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	assert.Equal(t, "alice-test", result.HumanIdentity)
	assert.Equal(t, "claude", result.AgentIdentity)
	assert.Equal(t, ".punt-labs/ethos.yaml", result.RepoConfig)
	assert.Equal(t, "foundation", result.Bundle)
	assert.NotNil(t, result.Skipped)
}

func TestSetup_SoloMode(t *testing.T) {
	home, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `
name: Solo Dev
handle: solo-dev
`)

	_, stderr, err := execHandler(t, "setup", "--file", cfgPath, "--solo")
	require.NoError(t, err, "stderr: %s", stderr)

	// Identities created.
	assert.FileExists(t, filepath.Join(home, ".punt-labs", "ethos", "identities", "solo-dev.yaml"))
	assert.FileExists(t, filepath.Join(home, ".punt-labs", "ethos", "identities", "claude.yaml"))

	// Repo config has agent but no team or active_bundle.
	configData, err := os.ReadFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(configData), "agent: claude")
	assert.NotContains(t, string(configData), "active_bundle")
	assert.NotContains(t, string(configData), "team:")

	// No agent files generated.
	_, err = os.ReadDir(filepath.Join(repo, ".claude", "agents"))
	assert.True(t, os.IsNotExist(err), "agents dir should not exist in solo mode")
}

func TestSetup_BundleFlag_Valid(t *testing.T) {
	_, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `name: Bundle User
handle: bundle-user
`)

	_, _, err := execHandler(t, "setup", "--file", cfgPath, "--bundle", "gstack")
	require.NoError(t, err)

	configData, err := os.ReadFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(configData), "active_bundle: gstack")
}

func TestSetup_BundleFlag_Invalid(t *testing.T) {
	_, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `name: Bad Bundle
handle: bad-bundle
`)

	_, _, err := execHandler(t, "setup", "--file", cfgPath, "--bundle", "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `bundle "nonexistent" not found`)
}

func TestSetup_Idempotent(t *testing.T) {
	_, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `
name: Repeat User
handle: repeat-user
`)

	// First run.
	_, _, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err)

	// Second run -- should succeed with skips.
	_, stderr, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err)
	assert.Contains(t, stderr, `skipped: identity "repeat-user" already exists`)
	assert.Contains(t, stderr, `skipped: identity "claude" already exists`)
	assert.Contains(t, stderr, `skipped: bundle "foundation" already active`)
}

func TestSetup_Idempotent_JSON(t *testing.T) {
	_, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `
name: Repeat JSON
handle: repeat-json
`)

	// First run.
	_, _, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err)

	// Second run with --json.
	stdout, _, err := execHandler(t, "setup", "--file", cfgPath, "--json")
	require.NoError(t, err)

	var result setupResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	assert.Contains(t, result.Skipped, "human_identity")
	assert.Contains(t, result.Skipped, "agent_identity")
}

func TestSetup_NoTTY_NoFile(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	home, repo := setupTestEnv(t)

	// Run the binary with stdin from /dev/null (not a TTY).
	cmd := exec.Command(ethosBinary, "setup")
	cmd.Dir = repo
	cmd.Env = []string{
		"HOME=" + home,
		"USER=test-user",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "expected non-zero exit")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected *exec.ExitError, got %T", err)
	assert.Equal(t, 2, exitErr.ExitCode(), "no-TTY should exit 2; output: %s", out)
	assert.Contains(t, string(out), "setup requires a terminal")
}

func TestSetup_NotInGitRepo(t *testing.T) {
	// t.TempDir uses TMPDIR which may be inside the repo (.tmp/).
	// Create an isolated dir under /tmp so FindRepoRoot finds no .git.
	noRepo, err := os.MkdirTemp("/tmp", "ethos-setup-norepo-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(noRepo) })

	home, err := os.MkdirTemp("/tmp", "ethos-setup-home-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(home) })

	// Reset flag state.
	setupBundle = "foundation"
	setupSolo = false
	setupFile = ""
	t.Cleanup(func() {
		setupBundle = "foundation"
		setupSolo = false
		setupFile = ""
	})

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-user")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(noRepo)

	// Seed so identities can be created.
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	skillsRoot := filepath.Join(home, ".claude", "skills")
	_, seedErr := seed.Seed(globalRoot, skillsRoot, false)
	require.NoError(t, seedErr)

	cfgPath := filepath.Join(noRepo, "setup.yaml")
	writeSetupFile(t, cfgPath, `
name: No Repo User
handle: no-repo
`)

	_, stderr, runErr := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, runErr, "setup outside git repo should succeed for identities")
	assert.Contains(t, stderr, "not in a git repository")

	// Identities created.
	assert.FileExists(t, filepath.Join(home, ".punt-labs", "ethos", "identities", "no-repo.yaml"))
	assert.FileExists(t, filepath.Join(home, ".punt-labs", "ethos", "identities", "claude.yaml"))

	// No repo config.
	_, statErr := os.Stat(filepath.Join(noRepo, ".punt-labs", "ethos.yaml"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestSetup_NameRequired(t *testing.T) {
	_, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `handle: no-name`)

	_, _, err := execHandler(t, "setup", "--file", cfgPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestSetup_HandleValidation(t *testing.T) {
	cases := []struct {
		name   string
		handle string
	}{
		{"uppercase and spaces", "BAD HANDLE"},
		{"trailing hyphen", "abc-"},
		{"leading hyphen", "-abc"},
		{"double hyphen only", "--"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := setupTestEnv(t)

			cfgPath := filepath.Join(repo, "setup.yaml")
			writeSetupFile(t, cfgPath, "name: Bad Handle\nhandle: "+tc.handle+"\n")

			_, _, err := execHandler(t, "setup", "--file", cfgPath)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "must be lowercase alphanumeric")
		})
	}
}

func TestSetup_DefaultHandle(t *testing.T) {
	home, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `name: Priya Chandran`)

	_, _, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err)

	// Should slugify the name to "priya-chandran".
	assert.FileExists(t, filepath.Join(home, ".punt-labs", "ethos", "identities", "priya-chandran.yaml"))
}

func TestSetup_MergeExistingConfig(t *testing.T) {
	_, repo := setupTestEnv(t)

	// Pre-create a repo config with a custom agent.
	writeRepoConfigFile(t, repo, "agent: custom-agent\n")

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `name: Merge User
handle: merge-user
`)

	_, _, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err)

	// Original agent key preserved.
	configData, err := os.ReadFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(configData), "agent: custom-agent")
	// New keys added.
	assert.Contains(t, string(configData), "active_bundle: foundation")
}

func TestSetup_LegacySubmoduleDetected(t *testing.T) {
	_, repo := setupTestEnv(t)

	// Create a fake legacy submodule.
	legacyDir := filepath.Join(repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(legacyDir, 0o755))
	writeGitmodulesLegacy(t, repo, "git@github.com:punt-labs/team.git")

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `name: Legacy User
handle: legacy-user
`)

	_, stderr, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err)
	assert.Contains(t, stderr, "legacy submodule detected")
	assert.Contains(t, stderr, "ethos team migrate")
}

func TestSetup_ResolveStyleChoice(t *testing.T) {
	attrs := []*attribute.Attribute{
		{Slug: "concise-quantified"},
		{Slug: "narrative"},
		{Slug: "conversational"},
	}
	tests := []struct {
		choice string
		want   string
	}{
		{"1", "concise-quantified"},
		{"2", "narrative"},
		{"3", "conversational"},
		{"narrative", "narrative"},
		{"custom-slug", "custom-slug"},
	}
	for _, tt := range tests {
		t.Run(tt.choice, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveStyleChoice(tt.choice, attrs))
		})
	}
}

func TestSetup_AgentWritingStyleDefault(t *testing.T) {
	home, repo := setupTestEnv(t)

	// No writing style specified -- agent should get concise-quantified.
	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `name: No Style
handle: no-style
`)

	_, _, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err)

	store := identity.NewStore(filepath.Join(home, ".punt-labs", "ethos"))
	agent, err := store.Load("claude", identity.Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "concise-quantified", agent.WritingStyle)
}

func TestSetup_HumanNoWritingStyle(t *testing.T) {
	home, repo := setupTestEnv(t)

	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, `name: Plain Jane
handle: plain-jane
`)

	_, _, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err)

	store := identity.NewStore(filepath.Join(home, ".punt-labs", "ethos"))
	human, err := store.Load("plain-jane", identity.Reference(true))
	require.NoError(t, err)
	assert.Empty(t, human.WritingStyle)
}

