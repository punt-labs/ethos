//go:build linux || darwin

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNightlyStopsBeforeLiveOnQuarantineFailure(t *testing.T) {
	dir := t.TempDir()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	for _, name := range []string{"scripts", "go.mod", "docs"} {
		require.NoError(t, os.Symlink(filepath.Join(root, name), filepath.Join(dir, name)))
	}
	base, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	overrides := `
test-quarantine:
	@echo "quarantine rejected"
	@exit 1
test-behavioral:
	@echo "live suite started"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), append(base, overrides...), 0o600))
	cmd := exec.CommandContext(t.Context(), "make", "-j2", "-k", "test-nightly")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", out)
	require.Contains(t, string(out), "quarantine rejected")
	require.NotContains(t, string(out), "live suite started")
	require.Contains(t, string(out), "nightly:")
}
