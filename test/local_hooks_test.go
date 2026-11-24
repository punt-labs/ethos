//go:build linux || darwin

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevelopmentPreCommit(t *testing.T) {
	for _, tc := range []struct {
		name, gate, want string
		fail             bool
	}{
		{name: "failed gate", gate: "false", want: "test-changed", fail: true},
		{name: "successful gate", gate: "true", want: "existing hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			home := t.TempDir()
			git := func(args ...string) ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Dir = dir
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
				return cmd.CombinedOutput()
			}
			out, err := git("init", "-q", "--template=")
			require.NoError(t, err, "%s", out)
			out, err = git("config", "core.hooksPath", ".githooks")
			require.NoError(t, err, "%s", out)
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".githooks"), 0o700))
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".git", "hooks"), 0o700))
			hook, err := os.ReadFile("../.githooks/pre-commit")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".githooks", "pre-commit"), hook, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\necho existing hook\n"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-changed:\n\t"+tc.gate+"\n"), 0o600))

			out, err = git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
				"commit", "--allow-empty", "-m", "fixture")
			if tc.fail {
				require.Error(t, err, "%s", out)
				require.NotContains(t, string(out), "existing hook")
			} else {
				require.NoError(t, err, "%s", out)
			}
			require.Contains(t, string(out), tc.want)
		})
	}
}
