package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSeed_UpgradeAndPreserve is the command-level walk of the manifest model:
// a fresh seed deploys; an edit to a tracked file is skipped as a local edit
// with the --force remedy printed; --force overwrites the edit and reports it
// as updated.
func TestSeed_UpgradeAndPreserve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Fresh seed deploys and tracks every file.
	stdout, err := execSeed(t, "seed")
	require.NoError(t, err)
	assert.Contains(t, stdout, "deployed:")
	assert.NotContains(t, stdout, "skipped (local edit):")

	// Edit a tracked role.
	rolePath := filepath.Join(home, ".punt-labs", "ethos", "roles", "implementer.yaml")
	require.NoError(t, os.WriteFile(rolePath, []byte("hand edited\n"), 0o644))

	// Plain re-seed preserves the edit and prints the remedy.
	stdout, err = execSeed(t, "seed")
	require.NoError(t, err)
	assert.Contains(t, stdout, "skipped (local edit):")
	assert.Contains(t, stdout, "implementer.yaml")
	assert.Contains(t, stdout, "--force")

	data, err := os.ReadFile(rolePath)
	require.NoError(t, err)
	assert.Equal(t, "hand edited\n", string(data), "a tracked edit must be preserved")

	// Force overwrites the edit and reports it as updated.
	stdout, err = execSeed(t, "seed", "--force")
	require.NoError(t, err)
	assert.Contains(t, stdout, "updated:")

	data, err = os.ReadFile(rolePath)
	require.NoError(t, err)
	assert.NotEqual(t, "hand edited\n", string(data), "force must overwrite the edit")
	assert.Contains(t, string(data), "name: implementer")
}

// TestSeed_FreshInstallDeploysEmbeddedBundleSkills pins the Bugbot HIGH
// finding on PR #481: a fresh install has active_bundle set in
// .punt-labs/ethos.yaml but the bundle isn't on disk yet anywhere
// bundle.ResolveActive looks (no repo-local override, no global bundle
// store from a prior seed). runSeed must still fall back to the
// config's bundle NAME and deploy that bundle's embedded skills — not
// silently no-op just because ResolveActive found nothing to resolve.
func TestSeed_FreshInstallDeploysEmbeddedBundleSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".punt-labs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"),
		[]byte("active_bundle: gstack\n"), 0o644))
	t.Setenv("ETHOS_REPO_ROOT", repoRoot)

	_, err := execSeed(t, "seed")
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(home, ".claude", "skills", "gstack-plan", "SKILL.md"))
	assert.FileExists(t, filepath.Join(home, ".claude", "skills", "gstack-ship", "SKILL.md"))
}

// TestSeed_NoRemedyWhenClean pins that the --force remedy line prints only when
// a local edit is skipped, not on a clean idempotent re-seed.
func TestSeed_NoRemedyWhenClean(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := execSeed(t, "seed")
	require.NoError(t, err)

	stdout, err := execSeed(t, "seed")
	require.NoError(t, err)
	assert.Contains(t, stdout, "unchanged:")
	assert.NotContains(t, stdout, "skipped (local edit):")
	assert.NotContains(t, stdout, "look locally edited")
}
