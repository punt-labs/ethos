//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/agentmark"
	"github.com/punt-labs/ethos/v4/plugin/hooks"
)

// execHookSessionStart runs the built `ethos hook session-start` in se's repo.
func execHookSessionStart(t *testing.T, se *cliSubprocessEnv) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(ethosBinary, "hook", "session-start")
	cmd.Dir = se.repo
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	cmd.Stdin = strings.NewReader(`{"session_id":"refresh-cli-session"}`)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	require.NoError(t, cmd.Run(), "stdout: %s\nstderr: %s", out.String(), errOut.String())
	return out.String(), errOut.String()
}

func TestCLI_SessionStartLeavesTrackedAgentsUnchanged(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	_, stderr, code := runCLI(t, se, "enable")
	require.Zero(t, code, stderr)
	stale := filepath.Join(se.repo, ".claude", "agents", "retired.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o755))
	require.NoError(t, os.WriteFile(stale, []byte(agentmark.Add("---\nname: retired\n---\n")), 0o644))
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = se.repo, se.env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s: %s", args, out)
		return string(out)
	}
	git("add", "-f", "--", ".claude/agents/retired.md")
	git("add", "--", ".gitignore", "CLAUDE.md", ".claude/settings.json", ".punt-labs/ethos")
	git("-c", "core.hooksPath=/dev/null", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "Enable ethos with declared agent")
	require.Empty(t, git("status", "--porcelain"), "fixture must begin clean")

	execHookSessionStart(t, se)
	require.Empty(t, git("status", "--porcelain"), "startup must not rewrite or prune tracked agents")
	_, stderr, code = runCLI(t, se, "enable", "--refresh")
	require.Zero(t, code, stderr)
	require.NoFileExists(t, stale, "explicit refresh must still prune retired agents")
}

func TestCLI_EnablePinsDepositedGuideUntilRefresh(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	_, stderr, code := runCLI(t, se, "enable")
	require.Zero(t, code, stderr)
	guide := filepath.Join(se.repo, ".punt-labs", "ethos", "CLAUDE.md")
	pinned := []byte("guide deposited by another version\n")
	require.NoError(t, os.WriteFile(guide, pinned, 0o644))
	_, stderr, code = runCLI(t, se, "enable")
	require.Zero(t, code, stderr)
	after, err := os.ReadFile(guide)
	require.NoError(t, err)
	require.Equal(t, pinned, after)
	_, stderr, code = runCLI(t, se, "enable", "--refresh")
	require.Zero(t, code, stderr)
	after, err = os.ReadFile(guide)
	require.NoError(t, err)
	require.NotEqual(t, pinned, after)
}

// A stale seal section in an enabled repo is rewritten by SessionStart, and
// the session is told; a dormant repo and a current section are left alone.
func TestCLI_SessionStartRefreshesStaleHook(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the current CLI binary must build; this acceptance must not skip")
	stale := "#!/bin/sh\n" +
		"# --- BEGIN " + hooks.SealTag + " ---\n" +
		"# " + hooks.SealIdent + "\n" +
		"ethos audit seal || exit 2\n" +
		"# --- END " + hooks.SealTag + " ---\n"

	t.Run("enabled repo", func(t *testing.T) {
		se := freshCLIEnv(t, "refresh-human")
		_, stderr, code := runCLI(t, se, "enable")
		require.Equal(t, 0, code, stderr)
		hookPath := filepath.Join(se.repo, ".git", "hooks", "pre-commit")
		current, err := os.ReadFile(hookPath)
		require.NoError(t, err)

		out, errOut := execHookSessionStart(t, se)
		assert.NotContains(t, errOut, "refreshed stale git hook", "a current section is a no-op")
		assert.NotContains(t, out, "refreshed stale git hook")

		require.NoError(t, os.WriteFile(hookPath, []byte(stale), 0o755))
		out, errOut = execHookSessionStart(t, se)
		assert.Contains(t, errOut, "ethos: session-start: refreshed stale git hook section(s) at session start: pre-commit")
		assert.Contains(t, out, "refreshed stale git hook section(s) at session start: pre-commit")
		after, err := os.ReadFile(hookPath)
		require.NoError(t, err)
		assert.Equal(t, string(current), string(after))
	})

	t.Run("dormant repo", func(t *testing.T) {
		se := freshCLIEnv(t, "refresh-human")
		hooksDir := filepath.Join(se.repo, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooksDir, 0o755))
		hookPath := filepath.Join(hooksDir, "pre-commit")
		require.NoError(t, os.WriteFile(hookPath, []byte(stale), 0o755))

		_, errOut := execHookSessionStart(t, se)
		assert.NotContains(t, errOut, "refreshed stale git hook")
		after, err := os.ReadFile(hookPath)
		require.NoError(t, err)
		assert.Equal(t, stale, string(after))
	})
}
