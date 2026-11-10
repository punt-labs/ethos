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
	// A binary that knows both subcommands answers --help for whichever it is asked.
	const knowsSubcommand = "echo 'Usage:'; echo \"  ethos hook $2 [flags]\"\n"
	// A binary from before commit-msg existed: pre-commit only.
	const onlyPreCommit = "if [ \"$2\" = pre-commit ]; then echo '  ethos hook pre-commit [flags]'; else echo '  ethos hook [command]'; fi\n"
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
		assert.Contains(t, r.Detail, "predates `ethos hook ")
	})

	t.Run("a binary that exits non-zero on --help FAILs", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("PATH", stubEthos(t, "exit 2\n"))
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "FAIL", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "predates `ethos hook ")
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

	t.Run("the detail names the resolved path and that git may differ", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		bin := stubEthos(t, knowsSubcommand)
		t.Setenv("PATH", bin)
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "PASS", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "resolved via doctor's PATH to "+filepath.Join(bin, "ethos"))
		assert.Contains(t, r.Detail, "git may resolve differently")
	})

	t.Run("two different binaries WARN and name both", func(t *testing.T) {
		home := t.TempDir()
		localBin := filepath.Join(home, ".local", "bin")
		require.NoError(t, os.MkdirAll(localBin, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(localBin, "ethos"), []byte("#!/bin/sh\n"+knowsSubcommand), 0o755))
		onPath := stubEthos(t, knowsSubcommand)
		t.Setenv("HOME", home)
		t.Setenv("PATH", onPath)
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "WARN", r.Status, r.Detail)
		assert.Contains(t, r.Detail, filepath.Join(onPath, "ethos"))
		assert.Contains(t, r.Detail, filepath.Join(localBin, "ethos"))
	})

	t.Run("one binary reached by two names does not WARN", func(t *testing.T) {
		home := t.TempDir()
		localBin := filepath.Join(home, ".local", "bin")
		require.NoError(t, os.MkdirAll(localBin, 0o755))
		onPath := stubEthos(t, knowsSubcommand)
		require.NoError(t, os.Symlink(filepath.Join(onPath, "ethos"), filepath.Join(localBin, "ethos")))
		t.Setenv("HOME", home)
		t.Setenv("PATH", onPath)
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "PASS", r.Status, r.Detail)
	})

	t.Run("a binary with pre-commit but not commit-msg FAILs naming commit-msg", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("PATH", stubEthos(t, onlyPreCommit))
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "FAIL", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "ethos hook commit-msg")
		assert.NotContains(t, r.Detail, "ethos hook pre-commit`")
		assert.Contains(t, r.Detail, "predates `ethos hook ")
	})

	t.Run("a binary that cannot be executed is not reported as needing an upgrade", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		bin := t.TempDir()
		// Executable bit set, but no interpreter and no ELF/Mach-O header: exec fails.
		require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte("not a program\n"), 0o755))
		t.Setenv("PATH", bin)
		r := CheckHookBinary(enabledRepoDir(t))
		assert.Equal(t, "FAIL", r.Status, r.Detail)
		assert.Contains(t, r.Detail, "cannot run")
		assert.NotContains(t, r.Detail, "predates")
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
