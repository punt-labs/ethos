//go:build linux || darwin

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHookPipeInput(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"hook", "pipe-input"})
	require.NoError(t, err)
	require.True(t, cmd.Hidden)
	t.Setenv("ETHOS_REPO_ROOT", "/missing/pipe-input-store")
	for _, payload := range []string{`{"tool_input":{"file_path":"file.go"}}`, "", "{invalid"} {
		t.Run(payload, func(t *testing.T) {
			rootCmd.SetIn(strings.NewReader(payload))
			t.Cleanup(func() { rootCmd.SetIn(nil) })
			out, stderr, err := execHandler(t, "hook", "pipe-input")
			require.NoError(t, err)
			require.Empty(t, stderr)
			require.Equal(t, payload, out)
		})
	}
}
