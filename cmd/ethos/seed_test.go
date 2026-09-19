package main

import (
	"os"
	"path/filepath"
	"runtime"
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

// TestSeed_SkipReasonOnStdout pins the silent-failure fix: a skip that
// additiveMerge actually evaluated and declined must say why, not just
// "exists" — the same ambiguity GH #525's own doctor remedy hit ("run ethos
// seed" against a skip category, with no way to tell "will fix itself" from
// "needs a hand-edit").
func TestSeed_SkipReasonOnStdout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A hand-edited implement.yaml predating the seed manifest: same fields
	// as shipped, but allow_empty_write_set genuinely disagrees — a
	// conflict, not a missing-field gap additiveMerge can repair.
	archDir := filepath.Join(home, ".punt-labs", "ethos", "archetypes")
	require.NoError(t, os.MkdirAll(archDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(archDir, "implement.yaml"), []byte(
		"name: implement\n"+
			"description: \"Implementation mission — output is code\"\n"+
			"budget_default:\n  rounds: 3\n  reflection_after_each: true\n"+
			"allow_empty_write_set: true\n"+
			"required_fields: []\n"+
			"write_set_constraints: []\n"+
			"extract_into_constraints: []\n"), 0o644))

	stdout, err := execSeed(t, "seed")
	require.NoError(t, err)
	assert.Contains(t, stdout, `skipped (exists; beyond additive repair: conflicting value for key "allow_empty_write_set"):`)
	assert.Contains(t, stdout, "implement.yaml")
}

// TestSeed_PrintsBucketsOnPartialFailure pins the silent-failure fix: seed
// mutates files in place as it goes, so a run that errors partway through
// must still report what it DID write, not just the errors — the prior
// behavior threw away every Deployed/Repaired/Updated line the moment any
// single file failed.
func TestSeed_PrintsBucketsOnPartialFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based permission denial is Unix-specific")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits — cannot reproduce EACCES as root")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Pre-create the roles directory read-only: every role file write fails,
	// while every other category (talents, personalities, ...) still
	// succeeds — errors and successful writes in the same run. The parent
	// dirs are created at normal permissions first — MkdirAll would
	// otherwise apply 0o500 to every directory it creates along the path,
	// blocking the manifest save too, not just the roles files.
	ethosRoot := filepath.Join(home, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosRoot, 0o755))
	rolesDir := filepath.Join(ethosRoot, "roles")
	require.NoError(t, os.Mkdir(rolesDir, 0o755))
	require.NoError(t, os.Chmod(rolesDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(rolesDir, 0o755) })

	stdout, err := execSeed(t, "seed")
	require.Error(t, err, "a blocked category must still surface as a command error")
	assert.Contains(t, stdout, "deployed:",
		"a partial-failure run must still report what it DID write, not just the error")
	assert.Contains(t, stdout, "talents",
		"a category unaffected by the blocked directory must still be reported")
	assert.Contains(t, stdout, "Seeded",
		"the summary line must still print on a partial failure")
}
