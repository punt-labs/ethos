//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitInitDir runs `git init` in dir so FindRepoRoot stops there rather
// than walking up past the temp dir into a real git ancestor.
func gitInitDir(t *testing.T, dir, home string) {
	t.Helper()
	cmd := exec.Command("git", "init", dir)
	cmd.Env = []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init %s: %s", dir, out)
}

// execHandler runs a cobra command in-process with stdout/stderr captured
// and args set. It resets package-level flag state at test end. Not safe
// for t.Parallel — it mutates shared rootCmd and global flags.
func execHandler(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	// Reset package-level flag state so tests do not leak into each
	// other via persistent cobra flag bindings.
	jsonOutput = false
	showReference = false
	whoamiReference = false
	t.Cleanup(func() {
		jsonOutput = false
		showReference = false
		whoamiReference = false
	})

	var outBuf, errBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs(args)
	defer func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	}()

	err = rootCmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// setInProcessEnv applies the subprocess env fixture to the current
// process via t.Setenv, which auto-restores at test end.
func setInProcessEnv(t *testing.T, se *cliSubprocessEnv) {
	t.Helper()
	t.Setenv("HOME", se.home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(se.repo)
}

// --- version ---

func TestRunVersion_Plain(t *testing.T) {
	stdout, _, err := execHandler(t, "version")
	require.NoError(t, err)
	assert.Contains(t, stdout, "ethos "+version)
}

func TestRunVersion_JSON(t *testing.T) {
	stdout, _, err := execHandler(t, "version", "--json")
	require.NoError(t, err)
	var parsed map[string]string
	require.NoError(t, json.Unmarshal([]byte(stdout), &parsed))
	assert.Equal(t, version, parsed["version"])
}

// --- doctor ---

func TestRunDoctor_AllPass(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "doctor")
	require.NoError(t, err, "expected all doctor checks to pass")
	// Four checks, all should PASS.
	assert.Contains(t, stdout, "Identity directory")
	assert.Contains(t, stdout, "Human identity")
	assert.Contains(t, stdout, "Default agent")
	assert.Contains(t, stdout, "Duplicate fields")
	assert.Contains(t, stdout, "PASS")
	assert.NotContains(t, stdout, "FAIL")
}

func TestRunDoctor_Failure(t *testing.T) {
	// Fixture that forces CheckHumanIdentity to fail: global store has
	// no identity matching USER. Git-init the cwd so FindRepoRoot does
	// not walk up into a real git ancestor.
	home := t.TempDir()
	globalIDs := filepath.Join(home, ".punt-labs", "ethos", "identities")
	require.NoError(t, os.MkdirAll(globalIDs, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".punt-labs", "ethos", "sessions"), 0o700))

	repo := t.TempDir()
	gitInitDir(t, repo, home)

	t.Setenv("HOME", home)
	t.Setenv("USER", "nobody")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	stdout, _, err := execHandler(t, "doctor")
	require.Error(t, err, "expected error when a check fails")
	assert.Contains(t, stdout, "FAIL")
}

func TestRunDoctor_JSON(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "doctor", "--json")
	require.NoError(t, err)
	var results []map[string]string
	require.NoError(t, json.Unmarshal([]byte(stdout), &results))
	require.Len(t, results, 4)
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r["name"]
	}
	assert.Contains(t, names, "Identity directory")
	assert.Contains(t, names, "Human identity")
}

// --- whoami ---

func TestRunWhoami_Plain(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "whoami")
	require.NoError(t, err)
	assert.Contains(t, stdout, "test-agent")
	assert.Contains(t, stdout, "Test Agent")
}

func TestRunWhoami_JSON(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "whoami", "--json")
	require.NoError(t, err)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &parsed))
	assert.Equal(t, "test-agent", parsed["handle"])
}

func TestRunWhoami_NoMatch(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)
	t.Setenv("USER", "stranger")

	_, _, err := execHandler(t, "whoami")
	require.Error(t, err, "expected error when USER has no matching identity")
}

// --- list ---

func TestRunList_Plain(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "HANDLE")
	assert.Contains(t, stdout, "NAME")
	assert.Contains(t, stdout, "KIND")
	assert.Contains(t, stdout, "test-agent")
}

func TestRunList_JSON(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "list", "--json")
	require.NoError(t, err)
	var ids []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &ids))
	require.Len(t, ids, 1)
	assert.Equal(t, "test-agent", ids[0]["handle"])
}

func TestRunList_Empty(t *testing.T) {
	home := t.TempDir()
	globalIDs := filepath.Join(home, ".punt-labs", "ethos", "identities")
	require.NoError(t, os.MkdirAll(globalIDs, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".punt-labs", "ethos", "sessions"), 0o700))

	repo := t.TempDir()
	gitInitDir(t, repo, home)

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	stdout, _, err := execHandler(t, "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "No identities found")
}

// --- show ---

func TestRunShow_Plain(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "show", "test-agent")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Test Agent")
	assert.Contains(t, stdout, "test-agent")
}

func TestRunShow_JSON(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "show", "test-agent", "--json")
	require.NoError(t, err)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &parsed))
	assert.Equal(t, "test-agent", parsed["handle"])
}

func TestRunShow_NotFound(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	_, _, err := execHandler(t, "show", "no-such-handle")
	require.Error(t, err, "expected error for unknown handle")
}

func TestRunShow_Reference(t *testing.T) {
	// --reference flag exercises the identity.Reference(true) code path
	// in runShow.
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "show", "test-agent", "--reference")
	require.NoError(t, err)
	assert.Contains(t, stdout, "test-agent")
}

func TestRunShow_WithAttributes(t *testing.T) {
	// Identity with personality, writing style, and talents exercises
	// the attribute-rendering branches at the bottom of runShow.
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	globalEthos := filepath.Join(se.home, ".punt-labs", "ethos")
	writeAttrFile(t, globalEthos, "writing-styles", "terse.md", "# Terse\nDirect prose.\n")
	writeAttrFile(t, globalEthos, "personalities", "analytical.md", "# Analytical\nData-driven.\n")
	writeAttrFile(t, globalEthos, "talents", "go.md", "# Go\nSystems programming.\n")

	idData := []byte(`name: Full Agent
handle: full-agent
kind: agent
email: full@example.test
github: full-agent
writing_style: terse
personality: analytical
talents:
  - go
`)
	path := filepath.Join(globalEthos, "identities", "full-agent.yaml")
	require.NoError(t, os.WriteFile(path, idData, 0o644))

	stdout, _, err := execHandler(t, "show", "full-agent")
	require.NoError(t, err)
	assert.Contains(t, stdout, "full-agent")
	assert.Contains(t, stdout, "Terse")
	assert.Contains(t, stdout, "Analytical")
	assert.Contains(t, stdout, "--- go ---")
	assert.Contains(t, stdout, "Systems programming")
}

// writeAttrFile writes a markdown attribute file under
// <ethosRoot>/<kind>/<name>.
func writeAttrFile(t *testing.T, ethosRoot, kind, name, content string) {
	t.Helper()
	dir := filepath.Join(ethosRoot, kind)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

// --- additional coverage ---

func TestRunWhoami_Reference(t *testing.T) {
	// --reference exercises the whoamiReference branch.
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "whoami", "--reference")
	require.NoError(t, err)
	assert.Contains(t, stdout, "test-agent")
}

func TestRunDoctor_FailureJSON(t *testing.T) {
	// Doctor failure with --json: writes JSON array before returning error.
	home := t.TempDir()
	globalIDs := filepath.Join(home, ".punt-labs", "ethos", "identities")
	require.NoError(t, os.MkdirAll(globalIDs, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".punt-labs", "ethos", "sessions"), 0o700))

	repo := t.TempDir()
	gitInitDir(t, repo, home)

	t.Setenv("HOME", home)
	t.Setenv("USER", "nobody")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	stdout, _, err := execHandler(t, "doctor", "--json")
	require.Error(t, err)
	var results []map[string]string
	require.NoError(t, json.Unmarshal([]byte(stdout), &results))
	assert.NotEmpty(t, results)
}

func TestRunResolveAgent_Malformed(t *testing.T) {
	// Malformed ethos.yaml causes ResolveAgent to return an error.
	home := t.TempDir()
	repo := t.TempDir()
	gitInitDir(t, repo, home)

	cfgDir := filepath.Join(repo, ".punt-labs")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(cfgDir, "ethos.yaml"),
		[]byte("agent: [not a string"),
		0o644))

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	_, _, err := execHandler(t, "resolve-agent")
	require.Error(t, err)
}

func TestRunList_EmptyJSON(t *testing.T) {
	home := t.TempDir()
	globalIDs := filepath.Join(home, ".punt-labs", "ethos", "identities")
	require.NoError(t, os.MkdirAll(globalIDs, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".punt-labs", "ethos", "sessions"), 0o700))

	repo := t.TempDir()
	gitInitDir(t, repo, home)

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	stdout, _, err := execHandler(t, "list", "--json")
	require.NoError(t, err)
	var ids []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &ids))
	assert.Empty(t, ids)
}

// --- resolve-agent ---

func TestRunResolveAgent_Configured(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	stdout, _, err := execHandler(t, "resolve-agent")
	require.NoError(t, err)
	assert.Contains(t, stdout, "test-agent")
}

func TestRunResolveAgent_NotConfigured(t *testing.T) {
	// Git-init repo without ethos.yaml: FindRepoRoot finds repo, but
	// ResolveAgent returns empty, so handler prints nothing and exits 0.
	home := t.TempDir()
	repo := t.TempDir()
	gitInitDir(t, repo, home)

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	stdout, _, err := execHandler(t, "resolve-agent")
	require.NoError(t, err)
	assert.Empty(t, stdout)
}
