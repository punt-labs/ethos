//go:build linux || darwin

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHookStdinWatchdog(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"hook", "stdin-watchdog"})
	require.NoError(t, err)
	require.True(t, cmd.Hidden)
	t.Setenv("ETHOS_REPO_ROOT", "/missing/watchdog-store")
	for _, pid := range []string{"bad", "0", "-1"} {
		out, stderr, err := execHandler(t, "hook", "stdin-watchdog", "--", pid)
		require.NoError(t, err)
		require.Empty(t, out)
		require.Empty(t, stderr)
	}
	require.Error(t, cmd.ValidateArgs(nil))
}
