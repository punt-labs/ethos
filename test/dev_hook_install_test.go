//go:build linux || darwin

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func developmentHookFixture(t *testing.T) (dir string, run func(string, string, ...string) ([]byte, error)) {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	bin := filepath.Join(dir, ".test-bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test", "PUNT_TELEMETRY=off"}
	run = func(at, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), name, args...)
		cmd.Dir, cmd.Env = at, env
		return cmd.CombinedOutput()
	}
	out, err := run(dir, "git", "init", "-q", "--template=")
	require.NoError(t, err, "%s", out)
	for _, name := range []string{"scripts", "go.mod", "docs", ".githooks"} {
		require.NoError(t, os.Symlink(filepath.Join(root, name), filepath.Join(dir, name)))
	}
	return dir, run
}

func installDevelopmentHooks(t *testing.T, dir string, run func(string, string, ...string) ([]byte, error)) {
	t.Helper()
	makefile, err := filepath.Abs("../Makefile")
	require.NoError(t, err)
	out, err := run(dir, "make", "-f", makefile, "hooks")
	require.NoError(t, err, "%s", out)
}

func TestDevelopmentHooksFutureWorktreeKeepsExistingHooks(t *testing.T) {
	dir, run := developmentHookFixture(t)
	out, err := run(dir, "git", "commit", "--allow-empty", "-m", "initial")
	require.NoError(t, err, "%s", out)
	hooks := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.Mkdir(hooks, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\necho 'existing pre-commit ran'\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "commit-msg"), []byte("#!/bin/sh\necho 'existing commit-msg ran'\n"), 0o700))
	installDevelopmentHooks(t, dir, run)
	linked := t.TempDir()
	out, err = run(dir, "git", "worktree", "add", "--detach", linked, "HEAD")
	require.NoError(t, err, "%s", out)
	out, err = run(linked, "git", "commit", "--allow-empty", "-m", "linked worktree")
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "existing pre-commit ran")
	require.Contains(t, string(out), "existing commit-msg ran")
}

func TestDevelopmentHooksDoctorPassesAfterOptIn(t *testing.T) {
	dir, run := developmentHookFixture(t)
	build := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(dir, ".test-bin", "ethos"), "./cmd/ethos")
	build.Dir = ".."
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s", out)
	out, err = run(dir, "ethos", "enable")
	require.NoError(t, err, "%s", out)
	installDevelopmentHooks(t, dir, run)
	out, err = run(dir, "ethos", "doctor")
	require.NoError(t, err, "%s", out)
	for _, label := range []string{"Audit seal hook", "Audit trailer hook", "Seal hook currency", "Trailer hook currency"} {
		require.Regexp(t, label+`[ ]+PASS`, string(out))
	}
}
