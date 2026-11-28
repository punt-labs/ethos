package enable

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersonalEnablementPreservesSharedIntegration(t *testing.T) {
	root := gitRepo(t)
	path := func(rel string) string { return filepath.Join(root, rel) }
	require.NoError(t, os.WriteFile(path(".gitignore"), []byte(".punt-labs/local/\nCLAUDE.local.md\n.claude/settings.local.json\n"), 0o644))
	_, err := EnableLocal(root, false)
	require.NoError(t, err)
	require.FileExists(t, path(resolve.LocalEnabledFile))
	require.NoFileExists(t, path(resolve.ProjectEnabledFile))
	require.FileExists(t, path(localSettings))
	require.FileExists(t, path(localHost))

	custom := []byte("custom personal guide\n")
	require.NoError(t, os.WriteFile(path(localGuide), custom, 0o644))
	_, err = EnableLocal(root, false)
	require.NoError(t, err)
	got, err := os.ReadFile(path(localGuide))
	require.NoError(t, err)
	assert.Equal(t, custom, got)
	_, err = EnableLocal(root, true)
	require.NoError(t, err)
	got, err = os.ReadFile(path(localGuide))
	require.NoError(t, err)
	assert.Equal(t, Guide, got)

	_, err = DisableLocal(root)
	require.NoError(t, err)
	require.NoFileExists(t, path(resolve.LocalEnabledFile))
	require.FileExists(t, path(localGuide))
	_, err = Enable(root)
	require.NoError(t, err)
	_, err = DisableLocal(root)
	require.NoError(t, err)
	require.FileExists(t, path(resolve.LocalDisabledFile))
	require.FileExists(t, path(resolve.ProjectEnabledFile))
	_, err = EnableLocal(root, false)
	require.NoError(t, err)
	require.NoFileExists(t, path(resolve.LocalDisabledFile))
	require.NoFileExists(t, path(resolve.LocalEnabledFile))

	_, err = DisableTo(root, root, false, true)
	require.NoError(t, err)
	require.FileExists(t, path(resolve.ProjectOptOutFile))
	_, err = EnableLocal(root, false)
	require.ErrorContains(t, err, resolve.ProjectOptOutFile)
	_, err = EnableTo(root, root, Options{ClearOptOut: true})
	require.NoError(t, err)
	require.NoFileExists(t, path(resolve.ProjectOptOutFile))
	require.FileExists(t, path(resolve.ProjectEnabledFile))
}

func TestPersonalEnableHonorsCloneLocalIgnoreRules(t *testing.T) {
	for _, source := range []string{".git/info/exclude", "core.excludesFile"} {
		t.Run(source, func(t *testing.T) {
			root := gitRepo(t)
			path := filepath.Join(root, ".git", "info", "exclude")
			if source == "core.excludesFile" {
				path = filepath.Join(t.TempDir(), "exclude")
				cmd := exec.Command("git", "-C", root, "config", source, path)
				require.NoError(t, cmd.Run())
			}
			require.NoError(t, os.WriteFile(path, []byte(".punt-labs/local/\nCLAUDE.local.md\n.claude/settings.local.json\n"), 0o644))
			_, err := EnableLocal(root, false)
			require.NoError(t, err)
			require.FileExists(t, filepath.Join(root, resolve.LocalEnabledFile))
			require.NoFileExists(t, filepath.Join(root, ".gitignore"))
		})
	}
}

func TestPersonalEnableKeepsLocksIgnoredWithFileSpecificRules(t *testing.T) {
	root := gitRepo(t)
	ignore := "/.punt-labs/local/ethos/CLAUDE.md\n/.punt-labs/local/ethos/enabled\n/.punt-labs/local/ethos/disabled\n/CLAUDE.local.md\n/.claude/settings.local.json\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(ignore), 0o644))
	gitRun(t, root, "add", ".gitignore")
	_, err := EnableLocal(root, false)
	require.NoError(t, err)
	cmd := exec.Command("git", "-C", root, "ls-files", "--others", "--exclude-standard")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Empty(t, string(out))
	assert.Equal(t, ignore, readFile(t, filepath.Join(root, ".gitignore")))
}

func TestPersonalEnablementRejectsUnignoredAndTrackedTargets(t *testing.T) {
	root := gitRepo(t)
	_, err := EnableLocal(root, false)
	require.ErrorContains(t, err, "ignored by git")
	require.NoFileExists(t, filepath.Join(root, localGuide))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".punt-labs/local/\nCLAUDE.local.md\n.claude/settings.local.json\n"), 0o644))
	file := filepath.Join(root, localHost)
	original := []byte("tracked user instructions")
	require.NoError(t, os.WriteFile(file, original, 0o644))
	gitRun(t, root, "add", "-f", localHost)
	_, err = EnableLocal(root, false)
	require.ErrorContains(t, err, "tracked by git")
	_, err = DisableLocal(root)
	require.ErrorContains(t, err, "tracked by git")
	got, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, original, got)
}

func TestPersonalEnableRollsBackWhenMarkerIsInvalid(t *testing.T) {
	root := gitRepo(t)
	path := func(rel string) string { return filepath.Join(root, rel) }
	require.NoError(t, os.WriteFile(path(".gitignore"), []byte(".punt-labs/local/\nCLAUDE.local.md\n.claude/settings.local.json\n"), 0o644))
	require.NoError(t, os.MkdirAll(path(resolve.LocalEnabledFile), 0o755))
	require.NoError(t, os.MkdirAll(path(".claude"), 0o755))
	originals := map[string]string{
		localHost:     "Personal instructions.\n",
		localGuide:    "Custom guide.\n",
		localSettings: "{\"permissions\":{\"allow\":[\"Bash(example:*)\"]}}\n",
	}
	for name, data := range originals {
		require.NoError(t, os.WriteFile(path(name), []byte(data), 0o640))
	}
	_, err := EnableLocal(root, true)
	require.ErrorContains(t, err, resolve.LocalEnabledFile)
	require.DirExists(t, path(resolve.LocalEnabledFile))
	for name, want := range originals {
		assert.Equal(t, want, readFile(t, path(name)), name)
		info, err := os.Stat(path(name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), name)
	}
}

func TestPersonalEnableRollsBackWhenMarkerSyncFails(t *testing.T) {
	root := gitRepo(t)
	path := func(rel string) string { return filepath.Join(root, rel) }
	require.NoError(t, os.WriteFile(path(".gitignore"), []byte(".punt-labs/local/\nCLAUDE.local.md\n.claude/settings.local.json\n"), 0o644))
	original := syncVendoredDestination
	t.Cleanup(func() { syncVendoredDestination = original })
	syncVendoredDestination = func(p layout.Policy, dest string) error {
		if dest == path(resolve.LocalEnabledFile) {
			return os.ErrPermission
		}
		return original(p, dest)
	}
	_, err := EnableLocal(root, false)
	require.ErrorIs(t, err, os.ErrPermission)
	for _, name := range []string{resolve.LocalEnabledFile, localGuide, localHost, localSettings} {
		require.NoFileExists(t, path(name))
	}
}

func TestPersonalEnablementLeavesInvalidInputsUntouched(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		content string
		dir     bool
		link    bool
	}{
		{name: "malformed opt-out", path: resolve.ProjectOptOutFile, dir: true},
		{name: "malformed project marker", path: resolve.ProjectEnabledFile, dir: true},
		{name: "unclosed instructions", path: localHost, content: "```\n"},
		{name: "invalid settings", path: localSettings, content: "not JSON"},
		{name: "guide directory", path: localGuide, dir: true},
		{name: "symlink", path: localHost, link: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitRepo(t)
			path := filepath.Join(root, tc.path)
			require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".punt-labs/local/\nCLAUDE.local.md\n.claude/settings.local.json\n"), 0o644))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			switch {
			case tc.dir:
				require.NoError(t, os.Mkdir(path, 0o755))
			case tc.link:
				target := filepath.Join(root, "CLAUDE.md")
				require.NoError(t, os.WriteFile(target, []byte("shared instructions"), 0o644))
				require.NoError(t, os.Symlink(target, path))
			default:
				require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o640))
			}
			_, err := EnableLocal(root, false)
			require.Error(t, err)
			require.NoFileExists(t, filepath.Join(root, resolve.LocalEnabledFile))
			require.NoFileExists(t, filepath.Join(root, resolve.LocalDisabledFile))
			switch {
			case tc.dir:
				require.DirExists(t, path)
			case tc.link:
				target, err := os.Readlink(path)
				require.NoError(t, err)
				assert.Equal(t, "shared instructions", readFile(t, target))
			default:
				assert.Equal(t, tc.content, readFile(t, path))
			}
		})
	}
}
