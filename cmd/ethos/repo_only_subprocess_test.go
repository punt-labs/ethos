//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setResolution rewrites the repo config with the given resolution
// value, keeping the agent binding the harness relies on.
func setResolution(t *testing.T, se *cliSubprocessEnv, mode string) {
	t.Helper()
	body := "agent: test-agent\n"
	if mode != "" {
		body += "resolution: " + mode + "\n"
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), []byte(body), 0o644))
}

// The whole point of repo-only, end to end: a handle that exists only in
// the user's home resolves in layered mode and does not in repo-only.
func TestCLI_RepoOnlyHidesGlobalIdentity(t *testing.T) {
	se := setupCLISubprocessEnv(t)

	setResolution(t, se, "")
	stdout, _, exit := runCLI(t, se, "show", "test-agent")
	require.Equal(t, 0, exit, "layered must still resolve the global identity")
	assert.Contains(t, stdout, "Test Agent")

	setResolution(t, se, "repo-only")
	_, stderr, exit := runCLI(t, se, "show", "test-agent")
	assert.Equal(t, 1, exit)
	assert.Contains(t, stderr, "repo-only")
}

// A typo in the enum must not degrade quietly to layered — that would
// leave the global fallback in place, which is the one thing repo-only
// exists to remove.
func TestCLI_UnknownResolutionIsFatal(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setResolution(t, se, "repo_only")

	_, stderr, exit := runCLI(t, se, "list")
	assert.Equal(t, 1, exit)
	assert.Contains(t, stderr, "unknown resolution")
}

// repo-only with no identity store is a hard startup error, not a
// silent fall-through to global.
func TestCLI_RepoOnlyWithoutStoreIsFatal(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setResolution(t, se, "repo-only")
	require.NoError(t, os.RemoveAll(filepath.Join(se.repo, ".punt-labs", "ethos")))

	_, stderr, exit := runCLI(t, se, "list")
	assert.Equal(t, 1, exit)
	assert.Contains(t, stderr, "no identity store")
}

// A REFUSED ETHOS_REPO_ROOT leaves StoreRepoRoot empty. If the mode were
// read from that empty root, the repo's own config would go unread and a
// repo-only repo would list global identities on a warning alone, exit
// 0 — laundering the refusal into the silent fallback repo-only forbids.
func TestCLI_RefusedRepoRootOverrideCannotFallBackToGlobal(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setResolution(t, se, "repo-only")
	require.NoError(t, os.RemoveAll(filepath.Join(se.repo, ".punt-labs", "ethos")))

	se.env = append(se.env, "ETHOS_REPO_ROOT="+se.repo)
	stdout, stderr, exit := runCLI(t, se, "list")

	assert.Equal(t, 1, exit, "must not succeed by falling back to global")
	assert.Contains(t, stderr, "no identity store")
	assert.NotContains(t, stdout, "test-agent")
}

// DES-057 Part C through the CLI: --local writes the companion file, the
// base file never absorbs the secret, and the read is the merged view.
func TestCLI_ExtLocalSplit(t *testing.T) {
	se := setupCLISubprocessEnv(t)

	_, stderr, exit := runCLI(t, se, "ext", "set", "test-agent", "quarry", "memory_collection", "mem")
	require.Equal(t, 0, exit, "stderr: %s", stderr)
	_, stderr, exit = runCLI(t, se, "ext", "set", "test-agent", "quarry", "api_token", "s3cret", "--local")
	require.Equal(t, 0, exit, "stderr: %s", stderr)

	extDir := filepath.Join(se.home, ".punt-labs", "ethos", "identities", "test-agent.ext")
	base, err := os.ReadFile(filepath.Join(extDir, "quarry.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(base), "memory_collection")
	assert.NotContains(t, string(base), "s3cret",
		"a base write must never fold the .local secret into the tracked file")

	local, err := os.ReadFile(filepath.Join(extDir, "quarry.local.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(local), "s3cret")

	stdout, _, exit := runCLI(t, se, "ext", "get", "test-agent", "quarry")
	require.Equal(t, 0, exit)
	assert.Contains(t, stdout, "mem")
	assert.Contains(t, stdout, "s3cret", "reads see the merged view")

	stdout, _, exit = runCLI(t, se, "ext", "list", "test-agent")
	require.Equal(t, 0, exit)
	assert.Contains(t, stdout, "quarry")
	assert.NotContains(t, stdout, "quarry.local", "no phantom namespace")
}
