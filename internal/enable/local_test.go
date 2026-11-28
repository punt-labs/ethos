package enable

import (
	"os"
	"path/filepath"
	"testing"

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
