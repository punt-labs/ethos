//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nestedGitEnv is an outer repository holding a store and an inner directory
// whose .git is built by mk.
func nestedGitEnv(t *testing.T, mk func(dot string) error) (se *cliSubprocessEnv, inner string) {
	t.Helper()
	se = setupCLISubprocessEnv(t)
	inner = filepath.Join(se.repo, "inner")
	require.NoError(t, os.Mkdir(inner, 0o755))
	if err := mk(filepath.Join(inner, ".git")); err != nil {
		t.Skipf("making .git: %v", err)
	}
	return se, inner
}

func createIdentityFrom(t *testing.T, se *cliSubprocessEnv, dir string) (string, int) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "id.yaml")
	require.NoError(t, os.WriteFile(file, []byte("name: Nested\nhandle: nested\nkind: agent\n"), 0o644))
	cmd := exec.Command(ethosBinary, "identity", "create", "--file", file)
	cmd.Dir = dir
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else {
		require.NoError(t, err)
	}
	return string(out), code
}

// A .git that git skips (dangling, empty, garbage) must not divert a write to
// the global store: git treats inner as part of outer, and so does ethos.
func TestCLI_InvalidInnerGitUsesOuterStore(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	cases := map[string]func(dot string) error{
		"dangling": func(dot string) error { return os.Symlink(filepath.Join(filepath.Dir(dot), "absent"), dot) },
		"empty":    func(dot string) error { return os.WriteFile(dot, nil, 0o644) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			se, inner := nestedGitEnv(t, mk)
			out, code := createIdentityFrom(t, se, inner)
			require.Equal(t, 0, code, out)
			assert.FileExists(t, filepath.Join(se.repo, ".punt-labs", "ethos", "identities", "nested.yaml"))
			assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "identities", "nested.yaml"))
		})
	}
}

// A .git git itself cannot read is a refusal, never a silent global write.
func TestCLI_UnreadableGitRefusesWrites(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, func(dot string) error { return os.Symlink(dot, dot) })
	out, code := createIdentityFrom(t, se, inner)
	assert.NotEqual(t, 0, code, out)
	assert.Contains(t, out, filepath.Join(inner, ".git"))
	assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "identities", "nested.yaml"))
	assert.NoFileExists(t, filepath.Join(se.repo, ".punt-labs", "ethos", "identities", "nested.yaml"))
}

// A command that reports on the degraded state stays runnable.
func TestCLI_DoctorRunsOnUnreadableGit(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, func(dot string) error { return os.Symlink(dot, dot) })
	cmd := exec.Command(ethosBinary, "doctor")
	cmd.Dir = inner
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	out, _ := cmd.CombinedOutput()
	assert.Contains(t, string(out), "Identity directory", "doctor still prints its table")
}

// Every command that reads or writes the store, plus seed, refuses on an
// unreadable .git instead of using the global store.
func TestCLI_UnreadableGitRefusesStoreCommands(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, func(dot string) error { return os.Symlink(dot, dot) })
	for _, args := range [][]string{
		{"seed"},
		{"vendor"},
		{"find", "missions"},
		{"team", "activate", "x"},
		{"role", "create", "x"},
		{"mission", "list"},
	} {
		t.Run(args[0], func(t *testing.T) {
			cmd := exec.Command(ethosBinary, args...)
			cmd.Dir = inner
			cmd.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, string(out))
			assert.Contains(t, string(out), filepath.Join(inner, ".git"))
		})
	}
}
