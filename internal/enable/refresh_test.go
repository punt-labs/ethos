package enable

import (
	"os"
	"path/filepath"
	"strings"
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

	t.Run("hooksPath inside the work tree is not rewritten and is reported", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		gitRun(t, root, "config", "core.hooksPath", ".husky")
		husky := filepath.Join(root, ".husky")
		require.NoError(t, os.MkdirAll(husky, 0o755))
		hookPath := filepath.Join(husky, "pre-commit")
		require.NoError(t, os.WriteFile(hookPath, []byte(staleSeal), 0o755))

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		assert.Equal(t, staleSeal, readFile(t, hookPath), "a tracked hook is never rewritten by a refresh")
		var tracked bool
		for _, st := range rep.Steps {
			if st.Step == "hook:pre-commit" && st.Status == StatusSkippedTracked {
				tracked = true
			}
		}
		assert.True(t, tracked, "steps: %+v", rep.Steps)
		assert.Contains(t, strings.Join(rep.Warnings, "\n"), hookPath)
		assert.Contains(t, strings.Join(rep.Warnings, "\n"), "ethos enable")
	})

	t.Run("a non-shell hook is reported as skipped", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		hookPath := filepath.Join(root, ".git", "hooks", "pre-commit")
		py := "#!/usr/bin/env python3\nprint('hi')\n"
		require.NoError(t, os.WriteFile(hookPath, []byte(py), 0o755))

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		assert.Equal(t, py, readFile(t, hookPath))
		require.Len(t, rep.Steps, 1)
		assert.Equal(t, StatusSkippedNonShell, rep.Steps[0].Status)
	})

	t.Run("a malformed hook does not stop the check of the next", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		hooksDir := filepath.Join(root, ".git", "hooks")
		require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"),
			[]byte("#!/bin/sh\n# --- BEGIN "+hooks.SealTag+" ---\n# "+hooks.SealIdent+"\n"+
				"# --- END "+hooks.SealTag+" ---\n# --- BEGIN "+hooks.SealTag+" ---\n# "+hooks.SealIdent+"\n"+
				"# --- END "+hooks.SealTag+" ---\n"), 0o755))
		commitMsg := filepath.Join(hooksDir, "commit-msg")
		want := readFile(t, commitMsg)
		stale := "#!/bin/sh\n# --- BEGIN " + hooks.TrailerTag + " ---\n# " + hooks.TrailerIdent + "\nexit 0\n# --- END " + hooks.TrailerTag + " ---\n"
		require.NoError(t, os.WriteFile(commitMsg, []byte(stale), 0o755))

		rep, err := RefreshHooks(root)
		require.Error(t, err, "the duplicated seal section is reported")
		assert.Equal(t, want, readFile(t, commitMsg), "the second hook is still refreshed")
		var refreshed bool
		for _, st := range rep.Steps {
			if st.Step == "hook:commit-msg" {
				refreshed = true
			}
		}
		assert.True(t, refreshed, "steps: %+v", rep.Steps)
	})

	t.Run("a missing hook is reported as installed", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		require.NoError(t, os.Remove(filepath.Join(root, ".git", "hooks", "commit-msg")))

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		require.Len(t, rep.Steps, 1)
		assert.Equal(t, "installed", rep.Steps[0].Status)
	})

	t.Run("a missing in-tree hook is not installed and is reported as such", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		gitRun(t, root, "config", "core.hooksPath", ".husky")
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".husky"), 0o755))

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		assert.NoFileExists(t, filepath.Join(root, ".husky", "pre-commit"))
		require.Len(t, rep.Steps, 2)
		assert.Equal(t, StatusSkippedTrackedMissing, rep.Steps[0].Status)
		assert.Contains(t, strings.Join(rep.Warnings, "\n"), "not installing it")
	})

	t.Run("a symlinked hook into the work tree is not rewritten", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		tracked := filepath.Join(root, "tools", "pre-commit")
		require.NoError(t, os.MkdirAll(filepath.Dir(tracked), 0o755))
		require.NoError(t, os.WriteFile(tracked, []byte(staleSeal), 0o755))
		hookPath := filepath.Join(root, ".git", "hooks", "pre-commit")
		require.NoError(t, os.Remove(hookPath))
		require.NoError(t, os.Symlink(tracked, hookPath))

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		assert.Equal(t, staleSeal, readFile(t, tracked), "the tracked target is byte-identical")
		var recorded bool
		for _, st := range rep.Steps {
			if st.Step == "hook:pre-commit" && st.Status == StatusSkippedTracked {
				recorded = true
			}
		}
		assert.True(t, recorded, "steps: %+v", rep.Steps)
	})

	t.Run("a dangling symlinked hook is an error and the other hook is still refreshed", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		hooksDir := filepath.Join(root, ".git", "hooks")
		commitMsg := filepath.Join(hooksDir, "commit-msg")
		want := readFile(t, commitMsg)
		require.NoError(t, os.WriteFile(commitMsg, []byte("#!/bin/sh\n# --- BEGIN "+hooks.TrailerTag+" ---\n# "+hooks.TrailerIdent+"\nexit 0\n# --- END "+hooks.TrailerTag+" ---\n"), 0o755))
		hookPath := filepath.Join(hooksDir, "pre-commit")
		require.NoError(t, os.Remove(hookPath))
		require.NoError(t, os.Symlink(filepath.Join(root, "missing"), hookPath))

		_, err = RefreshHooks(root)
		require.Error(t, err)
		assert.Equal(t, want, readFile(t, commitMsg))
	})

	t.Run("a hooks dir outside the repo is untouched", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		shared := physicalTempDir(t)
		hookPath := filepath.Join(shared, "pre-commit")
		require.NoError(t, os.WriteFile(hookPath, []byte(staleSeal), 0o755))
		gitRun(t, root, "config", "core.hooksPath", shared)

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		assert.Equal(t, staleSeal, readFile(t, hookPath))
		var recorded bool
		for _, st := range rep.Steps {
			if st.Step == "hook:pre-commit" && st.Status == StatusSkippedShared {
				recorded = true
			}
		}
		assert.True(t, recorded, "steps: %+v", rep.Steps)
		assert.Contains(t, strings.Join(rep.Warnings, "\n"), "outside this repo")
	})

	t.Run("a symlinked hook to a file outside the repo is untouched", func(t *testing.T) {
		root := gitRepo(t)
		_, err := Enable(root)
		require.NoError(t, err)
		outside := filepath.Join(physicalTempDir(t), "dotfiles-pre-commit")
		require.NoError(t, os.WriteFile(outside, []byte(staleSeal), 0o755))
		hookPath := filepath.Join(root, ".git", "hooks", "pre-commit")
		require.NoError(t, os.Remove(hookPath))
		require.NoError(t, os.Symlink(outside, hookPath))

		rep, err := RefreshHooks(root)
		require.NoError(t, err)
		assert.Equal(t, staleSeal, readFile(t, outside))
		var recorded bool
		for _, st := range rep.Steps {
			if st.Step == "hook:pre-commit" && st.Status == StatusSkippedShared {
				recorded = true
			}
		}
		assert.True(t, recorded, "steps: %+v", rep.Steps)
		assert.Contains(t, strings.Join(rep.Warnings, "\n"), outside)
	})

	t.Run("a linked worktree's hook symlinked into the main checkout is untouched", func(t *testing.T) {
		main := gitRepo(t)
		gitRun(t, main, "commit", "--allow-empty", "-q", "-m", "init")
		wt := filepath.Join(physicalTempDir(t), "wt")
		gitRun(t, main, "worktree", "add", "-q", wt)
		_, err := EnableTo(wt, main)
		require.NoError(t, err)
		tracked := filepath.Join(main, "tools", "pre-commit")
		require.NoError(t, os.MkdirAll(filepath.Dir(tracked), 0o755))
		require.NoError(t, os.WriteFile(tracked, []byte(staleSeal), 0o755))
		hookPath := filepath.Join(main, ".git", "hooks", "pre-commit")
		require.NoError(t, os.Remove(hookPath))
		require.NoError(t, os.Symlink(tracked, hookPath))

		rep, err := RefreshHooksTo(wt, main)
		require.NoError(t, err)
		assert.Equal(t, staleSeal, readFile(t, tracked))
		var recorded bool
		for _, st := range rep.Steps {
			if st.Step == "hook:pre-commit" && st.Status == StatusSkippedTracked {
				recorded = true
			}
		}
		assert.True(t, recorded, "steps: %+v", rep.Steps)
	})
}
