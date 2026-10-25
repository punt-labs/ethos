//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func emptyGitFile(dot string) error { return os.WriteFile(dot, nil, 0o644) }

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

// A .git that git skips (a dangling symlink, an empty directory, a symlink
// loop) must not divert a write to the global store: git treats inner as part of
// outer, and so does ethos.
func TestCLI_SkippedInnerGitUsesOuterStore(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	cases := map[string]func(dot string) error{
		"dangling": func(dot string) error { return os.Symlink(filepath.Join(filepath.Dir(dot), "absent"), dot) },
		"empty":    func(dot string) error { return os.Mkdir(dot, 0o755) },
		"loop":     func(dot string) error { return os.Symlink(dot, dot) },
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

// A .git git refuses (an empty or garbage gitfile, a gitdir to nowhere) is a
// refusal: git stops there, so ethos must not write to the outer or global store.
func TestCLI_InvalidInnerGitRefuses(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	cases := map[string]func(dot string) error{
		"empty":   emptyGitFile,
		"garbage": func(dot string) error { return os.WriteFile(dot, []byte("not a gitfile\n"), 0o644) },
		"nowhere": func(dot string) error { return os.WriteFile(dot, []byte("gitdir: /nonexistent\n"), 0o644) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			se, inner := nestedGitEnv(t, mk)
			out, code := createIdentityFrom(t, se, inner)
			assert.NotEqual(t, 0, code, out)
			assert.Contains(t, out, filepath.Join(inner, ".git"))
			assert.NoFileExists(t, filepath.Join(se.repo, ".punt-labs", "ethos", "identities", "nested.yaml"))
			assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "identities", "nested.yaml"))
		})
	}
}

// A .git git refuses is a refusal, never a silent global write.
func TestCLI_UnreadableGitRefusesWrites(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	out, code := createIdentityFrom(t, se, inner)
	assert.NotEqual(t, 0, code, out)
	assert.Contains(t, out, filepath.Join(inner, ".git"))
	assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "identities", "nested.yaml"))
	assert.NoFileExists(t, filepath.Join(se.repo, ".punt-labs", "ethos", "identities", "nested.yaml"))
}

// A command that reports on the degraded state stays runnable, and exits
// non-zero with a FAIL row: every non-exempt command would refuse here.
func TestCLI_DoctorRunsOnUnreadableGit(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	cmd := exec.Command(ethosBinary, "doctor")
	cmd.Dir = inner
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	out, err := cmd.CombinedOutput()
	assert.Contains(t, string(out), "Identity directory", "doctor still prints its table")
	assert.Contains(t, string(out), "Repository store")
	assert.Contains(t, string(out), "not located: ")
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, string(out))
	assert.NotEqual(t, 0, ee.ExitCode())
}

// Every command that reads or writes the store, plus seed, refuses on an
// unreadable .git instead of using the global store.
func TestCLI_UnreadableGitRefusesStoreCommands(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	for _, args := range [][]string{
		{"seed"},
		{"vendor"},
		{"find", "missions"},
		{"team", "activate", "x"},
		{"role", "create", "x"},
		{"mission", "list"},
		{"identity", "list"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.Command(ethosBinary, args...)
			cmd.Dir = inner
			cmd.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, string(out))
			assert.Contains(t, string(out), filepath.Join(inner, ".git"))
			assert.NotContains(t, string(out), "ethos: ethos", "the command path is not prefixed twice")
		})
	}
}

// The commands whose stores are global-only — session (whose write-current is
// doctor's own remedy), adr, whoami, iam — run on an unreadable .git: none of
// them refuses on the repository store.
func TestCLI_GlobalOnlyCommandsRunOnUnreadableGit(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	for _, args := range [][]string{
		{"whoami"},
		{"identity", "whoami"},
		{"iam", "x"},
		{"session"},
		{"session", "write-current", "--pid", "1", "--session", "s"},
		{"adr", "list"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.Command(ethosBinary, args...)
			cmd.Dir = inner
			cmd.Env = withForcedNotUnderClaudeCode(se.env)
			out, _ := cmd.CombinedOutput()
			assert.NotContains(t, string(out), "refusing to use the global store", string(out))
		})
	}
}

// The commands that report on, repair or do not use the store stay runnable.
func TestStoreRefusalExemptions(t *testing.T) {
	root := rootCmd
	exempt := [][]string{
		{"version"}, {"doctor"}, {"setup"}, {"seed"}, {"enable"}, {"disable"},
		{"hook"}, {"serve"}, {"uninstall"}, {"completion"}, {"audit"}, {"role", "schema"},
		{"whoami"}, {"iam"}, {"identity", "whoami"}, {"session"}, {"session", "write-current"}, {"adr"},
	}
	refused := [][]string{
		{"identity", "list"}, {"mission", "list"},
	}
	find := func(args []string) *cobra.Command {
		c, _, err := root.Find(args)
		require.NoError(t, err, args)
		return c
	}
	for _, args := range exempt {
		assert.True(t, exemptFromStoreRefusal(find(args)), "%v is exempt", args)
	}
	for _, args := range refused {
		assert.False(t, exemptFromStoreRefusal(find(args)), "%v refuses", args)
	}
}
