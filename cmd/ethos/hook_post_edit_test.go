//go:build linux || darwin

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHookPostEditFailOpen(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"hook", "post-edit"})
	require.NoError(t, err)
	require.True(t, cmd.Hidden)
	rootCmd.SetIn(strings.NewReader("{bad"))
	t.Cleanup(func() { rootCmd.SetIn(nil) })
	t.Setenv("ETHOS_REPO_ROOT", "/missing/post-edit-store")
	out, stderr, err := execHandler(t, "hook", "post-edit", "--agent", "bwk")
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Contains(t, out, `"hookEventName":"PostToolUse"`)
	require.Contains(t, out, "post-edit input:")
}
