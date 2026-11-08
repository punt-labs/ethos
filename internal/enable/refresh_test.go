package enable

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/plugin/hooks"
)

const staleSeal = "#!/bin/sh\n" +
	"# --- BEGIN " + hooks.SealTag + " ---\n" +
	"# " + hooks.SealIdent + "\n" +
	"ethos audit seal || exit 2\n" +
	"# --- END " + hooks.SealTag + " ---\n"

func TestRefreshHooks(t *testing.T) {
	t.Run("stale section is rewritten and only hooks are touched", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		hookPath := filepath.Join(root, ".git", "hooks", "pre-commit")
		want := readFile(t, hookPath)
		require.NoError(t, os.WriteFile(hookPath, []byte(staleSeal), 0o755))
		before := readFile(t, filepath.Join(root, ".gitignore"))

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		assert.Equal(t, want, readFile(t, hookPath))
		require.Len(t, rep.Steps, 1)
		assert.Equal(t, "hook:pre-commit", rep.Steps[0].Step)
		assert.Equal(t, before, readFile(t, filepath.Join(root, ".gitignore")))
	})

	t.Run("current sections are a no-op", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		hookPath := filepath.Join(root, ".git", "hooks", "pre-commit")
		info, err := os.Stat(hookPath)
		require.NoError(t, err)

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		assert.Empty(t, rep.Steps)
		after, err := os.Stat(hookPath)
		require.NoError(t, err)
		assert.Equal(t, info.ModTime(), after.ModTime(), "a current hook is not rewritten")
	})

	t.Run("dormant repo without the marker is untouched", func(t *testing.T) {
		root := gitRepo(t)
		hooksDir := filepath.Join(root, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooksDir, 0o755))
		hookPath := filepath.Join(hooksDir, "pre-commit")
		require.NoError(t, os.WriteFile(hookPath, []byte(staleSeal), 0o755))

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		assert.Empty(t, rep.Steps)
		assert.Equal(t, staleSeal, readFile(t, hookPath))
		assert.NoFileExists(t, filepath.Join(root, guideRel))
	})
}
