//go:build linux || darwin

package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enabledRepoDir is a directory carrying the enabled marker. CheckHookBinary
// reads only the marker, so it needs no git repository.
func enabledRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".punt-labs", "ethos", "enabled"), nil, 0o644))
	return dir
}

// stubEthos writes an executable ethos script into a new directory and returns it.
func stubEthos(t *testing.T, body string) string {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte("#!/bin/sh\n"+body), 0o755))
	return bin
}

func TestCheckHookBinary(t *testing.T) {
	const knowsSubcommand = "echo 'Usage:'; echo '  ethos hook pre-commit [flags]'\n"
	const groupHelp = "echo 'Usage:'; echo '  ethos hook [command]'\n"

	t.Run("not in a repo", func(t *testing.T) {
		r := CheckHookBinary("")
		assert.Equal(t, "PASS", r.Status)
	})

	t.Run("not enabled never fails, whatever the binary", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("PATH", t.TempDir())
		r := CheckHookBinary(t.TempDir())
		assert.Equal(t, "PASS", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "not enabled")
	})

	t.Run("an unusable marker is a FAIL, not dormancy", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".punt-labs", "ethos", "enabled"), 0o755))
		r := CheckHookBinary(dir)
		assert.Equal(t, "FAIL", r.Status, r.Detail)
	})

	t.Run("enabled with no binary FAILs", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("PATH", t.TempDir())
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "FAIL", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "no ethos binary")
	})

	t.Run("enabled with a current binary on PATH PASSes", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		bin := stubEthos(t, knowsSubcommand)
		t.Setenv("PATH", bin)
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "PASS", r.Status, r.Detail)
		assert.Contains(t, r.Detail, filepath.Join(bin, "ethos"))
	})

	t.Run("enabled with a binary predating the subcommand FAILs and names the remedy", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("PATH", stubEthos(t, groupHelp))
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "FAIL", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "upgrade ethos")
	})

	t.Run("a binary that exits non-zero on --help FAILs", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("PATH", stubEthos(t, "exit 2\n"))
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "FAIL", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "upgrade ethos")
	})

	t.Run("falls back to the default install directory like the shim", func(t *testing.T) {
		home := t.TempDir()
		localBin := filepath.Join(home, ".local", "bin")
		require.NoError(t, os.MkdirAll(localBin, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(localBin, "ethos"), []byte("#!/bin/sh\n"+knowsSubcommand), 0o755))
		t.Setenv("HOME", home)
		t.Setenv("PATH", t.TempDir())
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "PASS", r.Status, r.Detail)
		assert.Contains(t, r.Detail, filepath.Join(localBin, "ethos"))
	})

	t.Run("a non-executable file in the default directory is not a binary", func(t *testing.T) {
		home := t.TempDir()
		localBin := filepath.Join(home, ".local", "bin")
		require.NoError(t, os.MkdirAll(localBin, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(localBin, "ethos"), []byte("#!/bin/sh\n"+knowsSubcommand), 0o644))
		t.Setenv("HOME", home)
		t.Setenv("PATH", t.TempDir())
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "FAIL", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "no ethos binary")
	})
}
