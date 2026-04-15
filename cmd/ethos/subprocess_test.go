//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// cliSubprocessEnv holds the filesystem layout and env vars for a
// subprocess test of ethos CLI commands.
type cliSubprocessEnv struct {
	home string   // fake HOME
	repo string   // fake repo root
	env  []string // child process environment
}

// setupCLISubprocessEnv creates the minimal filesystem the ethos binary
// needs: a global identity store, a repo with config, and a real git init
// so FindRepoRoot stops at the fake repo rather than the real one.
func setupCLISubprocessEnv(t *testing.T) *cliSubprocessEnv {
	t.Helper()

	home := t.TempDir()
	repo := t.TempDir()

	// Global identity store.
	globalEthos := filepath.Join(home, ".punt-labs", "ethos")
	globalIDs := filepath.Join(globalEthos, "identities")
	require.NoError(t, os.MkdirAll(globalIDs, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(globalEthos, "sessions"), 0o700))

	idData, err := yaml.Marshal(map[string]interface{}{
		"name":   "Test Agent",
		"handle": "test-agent",
		"kind":   "agent",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(globalIDs, "test-agent.yaml"), idData, 0o644))

	// Repo: git init so FindRepoRoot stops here.
	repoEthos := filepath.Join(repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(filepath.Join(repoEthos, "identities"), 0o755))

	gitEnv := []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	gitInit := exec.Command("git", "init", repo)
	gitInit.Env = gitEnv
	initOut, initErr := gitInit.CombinedOutput()
	require.NoError(t, initErr, "git init failed: %s", initOut)

	// Repo config pointing at test-agent.
	cfgData, err := yaml.Marshal(map[string]string{"agent": "test-agent"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), cfgData, 0o644))

	env := []string{
		"HOME=" + home,
		// USER matches the identity handle so resolve.Resolve step 4
		// ($USER → handle field) resolves whoami without a git config.
		"USER=test-agent",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}

	return &cliSubprocessEnv{home: home, repo: repo, env: env}
}

// runCLI spawns ethosBinary with the given args, waits up to 5 seconds,
// and returns stdout, stderr, and the exit code. It does not call t.Fatal
// on non-zero exit — many tests assert the exit code directly.
func runCLI(t *testing.T, se *cliSubprocessEnv, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmd := exec.Command(ethosBinary, args...)
	if se != nil {
		cmd.Dir = se.repo
		cmd.Env = se.env
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	require.NoError(t, cmd.Start())

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case waitErr := <-done:
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				return outBuf.String(), errBuf.String(), exitErr.ExitCode()
			}
			t.Fatalf("ethos %v: unexpected error: %v", args, waitErr)
		}
		return outBuf.String(), errBuf.String(), 0
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("ethos %v hung — did not exit within 5 seconds\nstderr: %s", args, errBuf.String())
		return "", "", -1
	}
}

func TestCLI_Version(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	stdout, _, exitCode := runCLI(t, nil, "version")
	assert.Equal(t, 0, exitCode)
	assert.NotEmpty(t, stdout, "version output should be non-empty")
	// Output is "ethos <version>" — accept semver or "dev" (test builds
	// skip ldflags, so the binary reports "dev" rather than a release tag).
	assert.Regexp(t, `ethos (\d+\.\d+|dev)`, stdout)
}

func TestCLI_Whoami(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupCLISubprocessEnv(t)
	stdout, stderr, exitCode := runCLI(t, se, "whoami")
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout, "test-agent")
}

func TestCLI_WhoamiJSON(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupCLISubprocessEnv(t)
	stdout, stderr, exitCode := runCLI(t, se, "whoami", "--json")
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	assert.Equal(t, 0, exitCode)
	require.NotEmpty(t, stdout, "stdout should contain JSON output")
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &result), "stdout should be valid JSON")
	assert.Equal(t, "test-agent", result["handle"])
}

func TestCLI_List(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupCLISubprocessEnv(t)
	stdout, stderr, exitCode := runCLI(t, se, "list")
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout, "test-agent")
}

func TestCLI_Show(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupCLISubprocessEnv(t)
	stdout, stderr, exitCode := runCLI(t, se, "show", "test-agent")
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout, "test-agent")
}

func TestCLI_ShowJSON(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupCLISubprocessEnv(t)
	stdout, stderr, exitCode := runCLI(t, se, "show", "test-agent", "--json")
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	assert.Equal(t, 0, exitCode)
	require.NotEmpty(t, stdout, "stdout should contain JSON output")
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &result), "stdout should be valid JSON")
	assert.Equal(t, "test-agent", result["handle"])
}

func TestCLI_ResolveAgent(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupCLISubprocessEnv(t)
	stdout, stderr, exitCode := runCLI(t, se, "resolve-agent")
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout, "test-agent")
}

// TestCLI_MissingActiveBundle verifies that a repo configured with an
// active_bundle that does not exist produces a fatal error on stderr
// rather than silently falling back to 2-layer resolution. Users with
// misconfigured bundles must see a diagnostic.
func TestCLI_MissingActiveBundle(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupCLISubprocessEnv(t)

	// Point active_bundle at a name that has no matching directory
	// in either repo or global scope.
	cfgData, err := yaml.Marshal(map[string]string{
		"agent":         "test-agent",
		"active_bundle": "does-not-exist",
	})
	require.NoError(t, err)
	cfgPath := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.WriteFile(cfgPath, cfgData, 0o644))

	_, stderr, exitCode := runCLI(t, se, "list")
	assert.NotEqual(t, 0, exitCode, "CLI must fail when active_bundle is missing")
	assert.Contains(t, stderr, "bundle resolution failed")
	assert.Contains(t, stderr, "does-not-exist")
}

func TestCLI_Doctor(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupCLISubprocessEnv(t)
	stdout, stderr, exitCode := runCLI(t, se, "doctor")
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	// doctor exits 0 on all-pass, 1 on any failure — both are valid.
	// The assertion is that it ran without panicking, produced output,
	// and emitted nothing to stderr (no Go panic trace).
	assert.True(t, exitCode == 0 || exitCode == 1,
		"doctor should exit 0 or 1, got %d", exitCode)
	assert.NotEmpty(t, stdout, "doctor should print results to stdout")
	assert.NotContains(t, stderr, "goroutine", "doctor should not panic")
}
