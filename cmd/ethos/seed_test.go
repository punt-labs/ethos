//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
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
	assert.Contains(t, stdout, "look locally edited and were left unchanged")

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
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"),
		[]byte("active_bundle: gstack\n"), 0o644))
	t.Setenv("ETHOS_REPO_ROOT", repoRoot)

	_, err := execSeed(t, "seed")
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(home, ".claude", "skills", "gstack-plan", "SKILL.md"))
	assert.FileExists(t, filepath.Join(home, ".claude", "skills", "gstack-ship", "SKILL.md"))
}

// A malformed repo config is not a reason to skip bundle skills silently: seed
// still runs, and says why no bundle-scoped skills were deployed.
func TestSeed_MalformedRepoConfigWarns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"),
		[]byte("active_bundle: [unterminated\n"), 0o644))
	t.Setenv("ETHOS_REPO_ROOT", repoRoot)

	var err error
	stderr := captureStderrFn(t, func() { _, err = execSeed(t, "seed") })
	require.NoError(t, err)
	assert.Contains(t, stderr, "seed: reading the repo config")
	assert.Contains(t, stderr, "parsing repo config")
	assert.Contains(t, stderr, "no bundle-scoped skills")
	assert.FileExists(t, filepath.Join(home, ".punt-labs", "ethos", "roles", "implementer.yaml"))
	assert.FileExists(t, filepath.Join(repoRoot, ".claude", "agents", "code-reviewer.md"))
	assert.NoDirExists(t, filepath.Join(home, ".claude", "skills", "gstack-plan"))
}

func TestSeed_MalformedConfigRepairInGitRepo(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ETHOS_REPO_ROOT", "")
	runGit(t, root, "init", "-q")
	t.Chdir(root)
	path := filepath.Join(root, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	broken := []byte("active_bundle: [unterminated\n")
	require.NoError(t, os.WriteFile(path, broken, 0o600))

	seedForce = false
	t.Cleanup(func() { seedForce = false; rootCmd.SetArgs(nil) })
	rootCmd.SetArgs([]string{"seed"})
	var err error
	var stdout string
	stderr := captureStderrFn(t, func() {
		stdout = captureStdout(t, func() { err = rootCmd.Execute() })
	})
	require.NoError(t, err)
	assert.Contains(t, stderr, "seed: reading the repo config")
	assert.Contains(t, stderr, "no bundle-scoped skills")
	assert.Contains(t, stdout, "Seeded")
	assert.FileExists(t, filepath.Join(home, ".punt-labs", "ethos", "roles", "implementer.yaml"))
	assert.FileExists(t, filepath.Join(root, ".claude", "agents", "code-reviewer.md"))
	assert.NoDirExists(t, filepath.Join(root, ".punt-labs", "ethos", "roles"))
	assert.NoDirExists(t, filepath.Join(home, ".claude", "skills", "gstack-plan"))
	assert.NoDirExists(t, filepath.Join(root, ".punt-labs", "local", "ethos", "trace"))
	entries, err := os.ReadDir(filepath.Join(root, ".punt-labs", "local", "ethos", "tmp"))
	require.NoError(t, err)
	assert.Empty(t, entries)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, broken, data, "seed repairs content, not the configuration")
}

func TestSeed_InvalidRecordPolicyIsNotConfigRepair(t *testing.T) {
	for _, body := range []string{
		"record: invalid\n", "record: Full\n", "record: null\n", "record:\n",
		"record: true\n", "record: 1\n", "record: [full]\n", "record: {mode: full}\n",
		"mode: &mode false\nrecord: *mode\n",
		"defaults: &defaults {record: invalid}\n<<: *defaults\n",
		"record: full\nrecord: blame\n",
		"record: invalid\nactive_bundle: [gstack]\n",
		"active_bundle: [gstack]\nrecord: invalid\n",
		"agent: one\nagent: two\nrecord: invalid\n",
	} {
		t.Run(body, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ETHOS_REPO_ROOT", root)
			path := filepath.Join(root, ".punt-labs", "ethos.yaml")
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
			var err error
			stderr := captureStderrFn(t, func() { _, err = execSeed(t, "seed") })
			require.Error(t, err)
			var fieldErr *layout.ValidationError
			require.ErrorAs(t, err, &fieldErr)
			assert.Equal(t, "record", fieldErr.Field)
			assert.NotContains(t, stderr, "no bundle-scoped skills")
			assert.NoDirExists(t, filepath.Join(home, ".punt-labs", "ethos"))
			assert.NoDirExists(t, filepath.Join(root, ".claude"))
			assert.NoDirExists(t, filepath.Join(root, ".punt-labs", "local"))
		})
	}
}

func TestSeed_UsesConfigurationErrorSnapshotWithoutReread(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		refuse     bool
	}{
		{"repair malformed bundle", "active_bundle: [unterminated\n", false},
		{"repair wrong bundle type", "record: full\nactive_bundle: [gstack]\n", false},
		{"refuse invalid record", "record: invalid\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ETHOS_REPO_ROOT", root)
			path := filepath.Join(root, ".punt-labs", "ethos.yaml")
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(tt.body), 0o600))
			loc := resolve.Roots()
			require.Error(t, loc.ConfigErr)
			require.NoError(t, loc.StoreErr)
			require.NoError(t, loc.EthosErr)
			require.NoError(t, loc.CheckoutErr)
			// A second read would now authorize bundle deployment and full mode.
			require.NoError(t, os.WriteFile(path, []byte("record: full\nactive_bundle: gstack\n"), 0o600))
			previous := cmdRoots.Swap(&repoRoots{Located: loc, Global: true})
			defer cmdRoots.Store(previous)
			seedForce = false
			var err error
			stderr := captureStderrFn(t, func() {
				captureStdout(t, func() { err = runSeed(seedCmd, nil) })
			})
			if tt.refuse {
				require.ErrorContains(t, err, "record")
				assert.NoDirExists(t, filepath.Join(home, ".punt-labs", "ethos"))
				assert.NoDirExists(t, filepath.Join(root, ".claude"))
				return
			}
			require.NoError(t, err)
			assert.Contains(t, stderr, "no bundle-scoped skills")
			assert.FileExists(t, filepath.Join(root, ".claude", "agents", "code-reviewer.md"))
			assert.NoDirExists(t, filepath.Join(home, ".claude", "skills", "gstack-plan"))
			assert.NoDirExists(t, filepath.Join(root, ".punt-labs", "local", "ethos", "trace"))
		})
	}
}

func TestSeed_LocationErrorsRemainFatal(t *testing.T) {
	failure := errors.New("untrustworthy repository location")
	for _, tt := range []struct {
		name string
		loc  resolve.Located
	}{
		{"store", resolve.Located{StoreErr: failure}},
		{"ethos path", resolve.Located{EthosErr: failure}},
		{"checkout", resolve.Located{CheckoutErr: failure}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			previous := cmdRoots.Swap(&repoRoots{Located: tt.loc, Global: true})
			defer cmdRoots.Store(previous)
			err := runSeed(seedCmd, nil)
			require.ErrorIs(t, err, failure)
			assert.NoDirExists(t, filepath.Join(home, ".punt-labs", "ethos"))
		})
	}
}

// A fresh install (the bundle named in the config is not on disk yet) is
// expected, and says nothing.
func TestSeed_FreshInstallBundleIsSilent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"),
		[]byte("active_bundle: gstack\n"), 0o644))
	t.Setenv("ETHOS_REPO_ROOT", repoRoot)

	var err error
	stderr := captureStderrFn(t, func() { _, err = execSeed(t, "seed") })
	require.NoError(t, err)
	assert.NotContains(t, stderr, "seed:")
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

// TestSeed_AdditiveRepairOnStdoutAndCount pins the printer for the additive
// repair path: no prior test covered it at all, so a regression that made
// it print nothing (or drop out of the "Seeded N files" totals) would have
// gone unnoticed — a repair that never appears on stdout or in the count is
// worse than a merely mislabeled one, since an operator scanning the output
// would see no sign anything happened to the file at all.
func TestSeed_AdditiveRepairOnStdoutAndCount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A pre-v4.19.0 implement.yaml missing require_delegated_worker: the
	// exact upgrader shape GH #525 fixes.
	archDir := filepath.Join(home, ".punt-labs", "ethos", "archetypes")
	require.NoError(t, os.MkdirAll(archDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(archDir, "implement.yaml"), []byte(
		"name: implement\n"+
			"description: \"Implementation mission — output is code\"\n"+
			"budget_default:\n  rounds: 3\n  reflection_after_each: true\n"+
			"allow_empty_write_set: false\n"+
			"required_fields: []\n"+
			"write_set_constraints: []\n"+
			"extract_into_constraints: []\n"), 0o644))

	stdout, err := execSeed(t, "seed")
	require.NoError(t, err)
	assert.Contains(t, stdout, "repaired (missing fields added):")
	assert.Contains(t, stdout, "implement.yaml")
	assert.Contains(t, stdout, ", 1 repaired,",
		"the one additive repair must be counted in the \"Seeded N files\" summary")

	data, err := os.ReadFile(filepath.Join(archDir, "implement.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "require_delegated_worker: true")
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
