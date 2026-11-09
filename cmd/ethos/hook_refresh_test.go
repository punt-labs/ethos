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
