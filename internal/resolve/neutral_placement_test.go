package resolve

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func placementGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitenv.Parsed()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func TestConfigBareNeutralPlacement(t *testing.T) {
	git, err := exec.LookPath("git")
	require.NoError(t, err, "actual Git is required")
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, ".git"), []byte("gitdir: missing\n"), 0o600))
	t.Chdir(base)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(base, "bad-global"))
	require.NoError(t, os.WriteFile(filepath.Join(base, "bad-global"), []byte("[core]\n bare = true\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "invalid-include"), []byte("invalid config\n"), 0o600))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.bare=true'")
	record := filepath.Join(base, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(record, 0o700))
	obstruction := filepath.Join(record, "not-a-directory")
	require.NoError(t, os.WriteFile(obstruction, []byte("preserve\n"), 0o600))
	t.Setenv("TMPDIR", obstruction)
	t.Setenv("TMP", obstruction)
	t.Setenv("TEMP", obstruction)
	common := filepath.Join(base, "common")
	require.NoError(t, os.Mkdir(common, 0o700))
	for _, tc := range []struct {
		name, config string
		bare, bad    bool
	}{
		{"bare", "[core]\n bare = true\n", true, false},
		{"nonbare", "[core]\n bare = false\n", false, false},
		{"unset", "[core]\n repositoryformatversion = 0\n", false, false},
		{"boolean without value", "[core]\n bare\n", true, false},
		{"invalid boolean", "[core]\n bare = not-a-bool\n", false, true},
		{"malformed config", "invalid config\n", false, true},
		// --file disables include expansion unless --includes is specified.
		{"file includes remain disabled", "[include]\n path = ../invalid-include\n[core]\n bare = false\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(filepath.Join(common, "config"), []byte(tc.config), 0o600))
			bare, err := configBare(git, common)
			if tc.bad {
				require.Error(t, err)
				assert.Contains(t, err.Error(), filepath.Join(common, "config"))
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.bare, bare)
			}
		})
	}
	t.Run("invalid user config still fails Git startup", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(base, "invalid-include"))
		_, err := configBare(git, common)
		require.Error(t, err)
	})
	t.Run("config read failure", func(t *testing.T) {
		require.NoError(t, os.Remove(filepath.Join(common, "config")))
		require.NoError(t, os.Mkdir(filepath.Join(common, "config"), 0o700))
		_, err := configBare(git, common)
		require.Error(t, err)
	})
}

func TestGitConfigNeutralPlacement(t *testing.T) {
	base := t.TempDir()
	system := filepath.Join(base, "system")
	global := filepath.Join(base, "global")
	require.NoError(t, os.WriteFile(system, []byte("[user]\n name = system\n signingkey = system-only\n"), 0o600))
	require.NoError(t, os.WriteFile(global, []byte("[include]\n path = included\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "included"), []byte("[user]\n name = included\n"), 0o600))
	t.Setenv("GIT_CONFIG_SYSTEM", system)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	repo := filepath.Join(base, "repo")
	require.NoError(t, os.Mkdir(repo, 0o700))
	placementGit(t, repo, "init", "-q")
	placementGit(t, repo, "config", "user.name", "repository")
	t.Chdir(repo)
	record := filepath.Join(repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(record, 0o700))
	t.Setenv("TMPDIR", record)
	t.Setenv("TMP", record)
	t.Setenv("TEMP", record)
	for _, tc := range []struct{ checkout, key, want string }{
		{"", "user.name", "included"},
		{"", "user.signingkey", "system-only"},
		{"", "user.missing", ""},
		{repo, "user.name", "repository"},
	} {
		got, err := GitConfig(tc.checkout, tc.key)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "command")
	got, err := GitConfig("", "user.name")
	require.NoError(t, err)
	assert.Equal(t, "command", got)
	got, err = GitConfig(repo, "user.name")
	require.NoError(t, err)
	assert.Equal(t, "repository", got, "checkout reads retain Parsed semantics")
	entries, err := os.ReadDir(record)
	require.NoError(t, err)
	assert.Empty(t, entries)
	// A global read must also work from a broken repository without fallback.
	require.NoError(t, os.Rename(filepath.Join(repo, ".git"), filepath.Join(repo, "saved-git")))
	require.NoError(t, os.Symlink(filepath.Join(repo, "missing"), filepath.Join(repo, ".git")))
	got, err = GitConfig("", "user.name")
	require.NoError(t, err)
	assert.Equal(t, "command", got)
	_, err = GitConfig(repo, "user.name")
	require.Error(t, err, "repository reads must still refuse dangling .git")
	require.NoError(t, os.WriteFile(global, []byte("invalid config\n"), 0o600))
	_, err = GitConfig("", "user.missing")
	require.Error(t, err, "malformed config must not become an unset key")
}

func TestNeutralPlacementStoreRoots(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("ETHOS_REPO_ROOT", "")
	for _, bare := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary common", true: "bare common"}[bare], func(t *testing.T) {
			base := t.TempDir()
			main := filepath.Join(base, "main")
			require.NoError(t, os.Mkdir(main, 0o700))
			placementGit(t, main, "init", "-q")
			placementGit(t, main, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "commit", "-q", "--allow-empty", "-m", "fixture")
			if bare {
				common := filepath.Join(base, "bare.git")
				placementGit(t, base, "clone", "-q", "--bare", main, common)
				main = common
			} else {
				loc := locate(main)
				require.NoError(t, loc.err)
				require.NoError(t, loc.loc.storeErr)
				assert.Equal(t, main, loc.loc.root)
				assert.Equal(t, main, loc.loc.store)
			}
			checkout := filepath.Join(base, "feature")
			placementGit(t, main, "worktree", "add", "-q", "-b", "feature", checkout)
			record := filepath.Join(checkout, ".punt-labs", "ethos")
			require.NoError(t, os.MkdirAll(record, 0o700))
			t.Setenv("TMPDIR", record)
			t.Setenv("TMP", record)
			t.Setenv("TEMP", record)
			loc := locate(checkout)
			require.NoError(t, loc.err)
			require.NoError(t, loc.loc.storeErr)
			assert.Equal(t, checkout, loc.loc.root)
			want := main
			if bare {
				want = checkout
			}
			assert.Equal(t, want, loc.loc.store)
		})
	}
}
