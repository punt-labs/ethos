package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/enable"
	"github.com/stretchr/testify/require"
)

// buildValidateContent compiles the validate-content binary once for the
// package's test run and returns its path.
func buildValidateContent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "validate-content")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "building validate-content: %s", out)
	return bin
}

// minimalRepo writes the smallest ethos tree validate-content will accept:
// an identity, and empty directories for every content kind it scans.
// resolution, if non-empty, is written to .punt-labs/ethos.yaml.
func minimalRepo(t *testing.T, resolution string) (repoRoot, ethosRoot string) {
	t.Helper()
	repoRoot = t.TempDir()
	ethosRoot = filepath.Join(repoRoot, ".punt-labs", "ethos")
	for _, dir := range []string{
		"identities", "talents", "personalities", "writing-styles", "roles", "teams", "agents",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(ethosRoot, dir), 0o755))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "docs", "ETHOS-SETUP.md"), enable.Setup, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ethosRoot, "identities", "alice.yaml"), []byte(
		"name: Alice\nhandle: alice\nkind: human\n"), 0o644))

	cfg := "agent: alice\nteam: engineering\n"
	if resolution != "" {
		cfg += "resolution: " + resolution + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"), []byte(cfg), 0o644))
	return repoRoot, ethosRoot
}

// globalIdentityWithInvalidRef writes an identity to globalRoot that fails
// referential integrity — its manager references itself as a talent slug
// that does not exist anywhere. Consulting the global layer must never let
// this identity's failure leak into a repo-only run.
func globalWithInvalidIdentity(t *testing.T) string {
	t.Helper()
	globalRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(globalRoot, "identities", "ghost.yaml"), []byte(
		"name: Ghost\nhandle: ghost\nkind: human\ntalents:\n  - nonexistent-talent\n"), 0o644))
	return globalRoot
}

func runValidateContent(t *testing.T, bin, repoRoot, ethosRoot, globalRoot string) (stdout string, err error) {
	t.Helper()
	cmd := exec.Command(bin, "-ethos-root", ethosRoot, "-global-root", globalRoot)
	cmd.Dir = repoRoot
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

func TestValidateContent_LayeredModeUnchanged(t *testing.T) {
	bin := buildValidateContent(t)
	repoRoot, ethosRoot := minimalRepo(t, "")
	globalRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o755))

	out, err := runValidateContent(t, bin, repoRoot, ethosRoot, globalRoot)
	require.NoErrorf(t, err, "layered mode (unset resolution) must still pass: %s", out)
}

func TestValidateContent_RepoOnlyExcludesGlobal(t *testing.T) {
	bin := buildValidateContent(t)
	repoRoot, ethosRoot := minimalRepo(t, "repo-only")
	globalRoot := globalWithInvalidIdentity(t)

	out, err := runValidateContent(t, bin, repoRoot, ethosRoot, globalRoot)
	require.NoErrorf(t, err, "resolution: repo-only must exclude the global identity, not fail on its bad reference: %s", out)
	require.NotContains(t, out, "ghost", "the global-only identity must not appear in validate-content output at all")
}

func TestValidateContent_LayeredModeConsultsGlobal(t *testing.T) {
	bin := buildValidateContent(t)
	repoRoot, ethosRoot := minimalRepo(t, "")
	globalRoot := globalWithInvalidIdentity(t)

	out, err := runValidateContent(t, bin, repoRoot, ethosRoot, globalRoot)
	require.Error(t, err, "layered mode must still consult global, so the bad global identity should fail the run")
	require.Contains(t, out, "ghost")
}
