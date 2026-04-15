//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bundleTestEnv is a minimal repo + global layout for testing the
// team-bundle CLI commands.
type bundleTestEnv struct {
	home       string
	repo       string
	globalRoot string
}

// setupBundleTestEnv creates HOME and a git-init'd repo, sets env vars,
// and chdirs into the repo. The cleanup is wired through t.Setenv /
// t.Chdir so the test process state is restored on exit.
func setupBundleTestEnv(t *testing.T) *bundleTestEnv {
	t.Helper()

	// Reset add-bundle and migrate flag state — cobra persists these
	// across in-process test runs.
	addBundleName = ""
	addBundleGlobal = false
	addBundleApply = false
	migrateName = ""
	migrateApply = false
	t.Cleanup(func() {
		addBundleName = ""
		addBundleGlobal = false
		addBundleApply = false
		migrateName = ""
		migrateApply = false
	})

	home := t.TempDir()
	repo := t.TempDir()
	gitInitDir(t, repo, home)

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-user")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	return &bundleTestEnv{
		home:       home,
		repo:       repo,
		globalRoot: filepath.Join(home, ".punt-labs", "ethos"),
	}
}

// mkBundleDir creates a bundle directory with an optional manifest.
func mkBundleDir(t *testing.T, root, name string, withManifest bool) string {
	t.Helper()
	p := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(p, 0o755))
	if withManifest {
		body := "name: " + name + "\nversion: 1\n"
		require.NoError(t, os.WriteFile(filepath.Join(p, "bundle.yaml"), []byte(body), 0o644))
	}
	return p
}

// writeRepoConfig writes .punt-labs/ethos.yaml under repo.
func writeRepoConfigFile(t *testing.T, repo, body string) {
	t.Helper()
	p := filepath.Join(repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

func readRepoConfigFile(t *testing.T, repo string) string {
	t.Helper()
	p := filepath.Join(repo, ".punt-labs", "ethos.yaml")
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(data)
}

// --- team available ---

func TestTeamAvailable_Empty(t *testing.T) {
	setupBundleTestEnv(t)
	stdout, _, err := execHandler(t, "team", "available")
	require.NoError(t, err)
	assert.Contains(t, stdout, "No bundles discovered")
}

func TestTeamAvailable_WithBundles(t *testing.T) {
	env := setupBundleTestEnv(t)
	mkBundleDir(t, filepath.Join(env.globalRoot, "bundles"), "gstack", true)
	mkBundleDir(t, filepath.Join(env.repo, ".punt-labs", "ethos-bundles"), "punt-labs", true)

	stdout, _, err := execHandler(t, "team", "available")
	require.NoError(t, err)
	assert.Contains(t, stdout, "gstack")
	assert.Contains(t, stdout, "punt-labs")
	assert.Contains(t, stdout, "global")
	assert.Contains(t, stdout, "repo")
}

func TestTeamAvailable_JSON(t *testing.T) {
	env := setupBundleTestEnv(t)
	mkBundleDir(t, filepath.Join(env.globalRoot, "bundles"), "gstack", true)
	writeRepoConfigFile(t, env.repo, "active_bundle: gstack\n")

	stdout, _, err := execHandler(t, "team", "available", "--json")
	require.NoError(t, err)
	var rows []availableRow
	require.NoError(t, json.Unmarshal([]byte(stdout), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "gstack", rows[0].Name)
	assert.Equal(t, "global", rows[0].Source)
	assert.True(t, rows[0].Active)
}

// --- team activate ---

func TestTeamActivate_Success(t *testing.T) {
	env := setupBundleTestEnv(t)
	mkBundleDir(t, filepath.Join(env.globalRoot, "bundles"), "gstack", true)

	stdout, _, err := execHandler(t, "team", "activate", "gstack")
	require.NoError(t, err)
	assert.Contains(t, stdout, "activated: gstack")

	body := readRepoConfigFile(t, env.repo)
	assert.Contains(t, body, "active_bundle: gstack")
}

func TestTeamActivate_NonExistent(t *testing.T) {
	setupBundleTestEnv(t)
	_, _, err := execHandler(t, "team", "activate", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `bundle "nope" not found`)
}

func TestTeamActivate_InvalidSlug(t *testing.T) {
	setupBundleTestEnv(t)
	_, _, err := execHandler(t, "team", "activate", "../etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid bundle name")
}

func TestTeamActivate_AlreadyActive(t *testing.T) {
	env := setupBundleTestEnv(t)
	mkBundleDir(t, filepath.Join(env.globalRoot, "bundles"), "gstack", true)
	writeRepoConfigFile(t, env.repo, "active_bundle: gstack\n")

	stdout, _, err := execHandler(t, "team", "activate", "gstack")
	require.NoError(t, err)
	assert.Contains(t, stdout, "already active")
}

// --- team active ---

func TestTeamActive_None(t *testing.T) {
	setupBundleTestEnv(t)
	stdout, _, err := execHandler(t, "team", "active")
	require.NoError(t, err)
	assert.Equal(t, "(none)\n", stdout)
}

func TestTeamActive_Legacy(t *testing.T) {
	env := setupBundleTestEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Join(env.repo, ".punt-labs", "ethos"), 0o755))

	stdout, _, err := execHandler(t, "team", "active")
	require.NoError(t, err)
	assert.Equal(t, "(legacy)\n", stdout)
}

func TestTeamActive_Set(t *testing.T) {
	env := setupBundleTestEnv(t)
	mkBundleDir(t, filepath.Join(env.globalRoot, "bundles"), "gstack", true)
	writeRepoConfigFile(t, env.repo, "active_bundle: gstack\n")

	stdout, _, err := execHandler(t, "team", "active")
	require.NoError(t, err)
	assert.Contains(t, stdout, "gstack")
	assert.Contains(t, stdout, "global")
}

func TestTeamActive_JSON_None(t *testing.T) {
	setupBundleTestEnv(t)
	stdout, _, err := execHandler(t, "team", "active", "--json")
	require.NoError(t, err)
	assert.Equal(t, "null\n", stdout)
}

// --- team deactivate ---

func TestTeamDeactivate_Idempotent(t *testing.T) {
	setupBundleTestEnv(t)
	stdout, _, err := execHandler(t, "team", "deactivate")
	require.NoError(t, err)
	assert.Contains(t, stdout, "no active bundle to deactivate")
}

func TestTeamDeactivate_Removes(t *testing.T) {
	env := setupBundleTestEnv(t)
	writeRepoConfigFile(t, env.repo, "agent: claude\nactive_bundle: gstack\nteam: punt-labs\n")

	stdout, _, err := execHandler(t, "team", "deactivate")
	require.NoError(t, err)
	assert.Contains(t, stdout, "deactivated: was gstack")

	body := readRepoConfigFile(t, env.repo)
	assert.NotContains(t, body, "active_bundle")
	assert.Contains(t, body, "agent: claude")
	assert.Contains(t, body, "team: punt-labs")
}

// --- setConfigKey ---

func TestSetConfigKey_PreservesOtherKeys(t *testing.T) {
	repo := t.TempDir()
	writeRepoConfigFile(t, repo, "agent: claude\nteam: punt-labs\n")

	require.NoError(t, setConfigKey(repo, "active_bundle", "gstack"))

	body := readRepoConfigFile(t, repo)
	assert.Contains(t, body, "agent: claude")
	assert.Contains(t, body, "team: punt-labs")
	assert.Contains(t, body, "active_bundle: gstack")
}

func TestSetConfigKey_UpdatesExisting(t *testing.T) {
	repo := t.TempDir()
	writeRepoConfigFile(t, repo, "agent: claude\nactive_bundle: old\n")

	require.NoError(t, setConfigKey(repo, "active_bundle", "new"))

	body := readRepoConfigFile(t, repo)
	assert.Contains(t, body, "agent: claude")
	assert.Contains(t, body, "active_bundle: new")
	assert.NotContains(t, body, "old")
}

func TestSetConfigKey_Removes(t *testing.T) {
	repo := t.TempDir()
	writeRepoConfigFile(t, repo, "agent: claude\nactive_bundle: gstack\n")

	require.NoError(t, setConfigKey(repo, "active_bundle", ""))

	body := readRepoConfigFile(t, repo)
	assert.Contains(t, body, "agent: claude")
	assert.NotContains(t, body, "active_bundle")
}

func TestSetConfigKey_CreatesFile(t *testing.T) {
	repo := t.TempDir()

	require.NoError(t, setConfigKey(repo, "active_bundle", "gstack"))

	body := readRepoConfigFile(t, repo)
	assert.Contains(t, body, "active_bundle: gstack")
}

func TestSetConfigKey_RemoveOnEmptyFile_NoOp(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, setConfigKey(repo, "active_bundle", ""))
	p := filepath.Join(repo, ".punt-labs", "ethos.yaml")
	_, err := os.Stat(p)
	assert.True(t, os.IsNotExist(err), "file should not be created when removing key from absent file")
}

// --- bundleNameFromURL ---

func TestBundleNameFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"git@github.com:acme/team.git", "team"},
		{"https://github.com/acme/team.git", "team"},
		{"https://github.com/acme/team", "team"},
		{"git@github.com:acme/Acme-Corp.git", "acme-corp"},
		{"team_with_underscore.git", "team-with-underscore"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			assert.Equal(t, tt.want, bundleNameFromURL(tt.url))
		})
	}
}

// --- team add-bundle ---

func TestTeamAddBundle_DryRun(t *testing.T) {
	setupBundleTestEnv(t)
	stdout, _, err := execHandler(t, "team", "add-bundle", "git@github.com:acme/team.git")
	require.NoError(t, err)
	assert.Contains(t, stdout, "dry-run")
	assert.Contains(t, stdout, "git submodule add")
	assert.Contains(t, stdout, "ethos-bundles/team")
}

func TestTeamAddBundle_GlobalDryRun(t *testing.T) {
	setupBundleTestEnv(t)
	stdout, _, err := execHandler(t, "team", "add-bundle",
		"git@github.com:acme/team.git", "--global", "--name", "acme")
	require.NoError(t, err)
	assert.Contains(t, stdout, "dry-run")
	assert.Contains(t, stdout, "git clone")
	assert.True(t, strings.Contains(stdout, "bundles/acme") || strings.Contains(stdout, "bundles\\acme"))
}

func TestTeamAddBundle_InvalidName(t *testing.T) {
	setupBundleTestEnv(t)
	_, _, err := execHandler(t, "team", "add-bundle", "git@github.com:acme/.git")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid bundle name")
}

// --- team migrate ---

// writeGitmodulesLegacy writes a .gitmodules at repo root with a single
// legacy entry pointing .punt-labs/ethos at url.
func writeGitmodulesLegacy(t *testing.T, repo, url string) {
	t.Helper()
	body := "[submodule \".punt-labs/ethos\"]\n\tpath = .punt-labs/ethos\n\turl = " + url + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitmodules"), []byte(body), 0o644))
}

// TestLegacySubmoduleURL_URLBeforePath verifies that the parser finds
// the url even when it appears before path within a section. Git config
// format does not mandate key order.
func TestLegacySubmoduleURL_URLBeforePath(t *testing.T) {
	repo := t.TempDir()
	body := "[submodule \".punt-labs/ethos\"]\n" +
		"\turl = git@github.com:punt-labs/team.git\n" +
		"\tpath = .punt-labs/ethos\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitmodules"), []byte(body), 0o644))

	url, err := legacySubmoduleURL(repo)
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:punt-labs/team.git", url)
}

// TestLegacySubmoduleURL_MultipleSections verifies that a non-matching
// section before the target does not leak its url into the result.
func TestLegacySubmoduleURL_MultipleSections(t *testing.T) {
	repo := t.TempDir()
	body := "[submodule \"other\"]\n" +
		"\tpath = vendor/other\n" +
		"\turl = git@github.com:acme/other.git\n" +
		"[submodule \".punt-labs/ethos\"]\n" +
		"\turl = git@github.com:punt-labs/team.git\n" +
		"\tpath = .punt-labs/ethos\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitmodules"), []byte(body), 0o644))

	url, err := legacySubmoduleURL(repo)
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:punt-labs/team.git", url)
}

func TestMigrate_NoSubmodule(t *testing.T) {
	setupBundleTestEnv(t)
	stdout, _, err := execHandler(t, "team", "migrate")
	require.NoError(t, err)
	assert.Contains(t, stdout, "nothing to migrate")
}

func TestMigrate_NoGitmodulesEntry(t *testing.T) {
	env := setupBundleTestEnv(t)
	// Directory exists but no .gitmodules entry → not a submodule.
	require.NoError(t, os.MkdirAll(filepath.Join(env.repo, ".punt-labs", "ethos"), 0o755))

	stdout, _, err := execHandler(t, "team", "migrate")
	require.NoError(t, err)
	assert.Contains(t, stdout, "nothing to migrate")
}

func TestMigrate_DryRun(t *testing.T) {
	env := setupBundleTestEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Join(env.repo, ".punt-labs", "ethos"), 0o755))
	writeGitmodulesLegacy(t, env.repo, "git@github.com:punt-labs/team.git")

	stdout, _, err := execHandler(t, "team", "migrate")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Would run:")
	assert.Contains(t, stdout, "git submodule deinit")
	assert.Contains(t, stdout, "git rm -f .punt-labs/ethos")
	assert.Contains(t, stdout, "git submodule add")
	assert.Contains(t, stdout, "ethos-bundles/team")
	assert.Contains(t, stdout, "active_bundle: team")
	assert.Contains(t, stdout, "--apply")

	// Dry-run must not touch the repo config.
	_, err = os.Stat(filepath.Join(env.repo, ".punt-labs", "ethos.yaml"))
	assert.True(t, os.IsNotExist(err), "dry-run must not write config")
}

func TestMigrate_CustomName(t *testing.T) {
	env := setupBundleTestEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Join(env.repo, ".punt-labs", "ethos"), 0o755))
	writeGitmodulesLegacy(t, env.repo, "git@github.com:punt-labs/team.git")

	stdout, _, err := execHandler(t, "team", "migrate", "--name", "punt-labs")
	require.NoError(t, err)
	assert.Contains(t, stdout, "ethos-bundles/punt-labs")
	assert.Contains(t, stdout, "active_bundle: punt-labs")
}

func TestMigrate_InvalidName(t *testing.T) {
	env := setupBundleTestEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Join(env.repo, ".punt-labs", "ethos"), 0o755))
	writeGitmodulesLegacy(t, env.repo, "git@github.com:punt-labs/team.git")

	_, _, err := execHandler(t, "team", "migrate", "--name", "Bad Name")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid bundle name")
}

func TestMigrate_AlreadyMigrated(t *testing.T) {
	env := setupBundleTestEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Join(env.repo, ".punt-labs", "ethos"), 0o755))
	writeGitmodulesLegacy(t, env.repo, "git@github.com:punt-labs/team.git")
	// Target bundle path already exists.
	require.NoError(t, os.MkdirAll(
		filepath.Join(env.repo, ".punt-labs", "ethos-bundles", "team"), 0o755))

	stdout, _, err := execHandler(t, "team", "migrate")
	require.NoError(t, err)
	assert.Contains(t, stdout, "migration already done")
}
