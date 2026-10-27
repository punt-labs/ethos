//go:build linux || darwin

package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runHookAt runs hook from dir (git runs hooks at the top of the work tree)
// with a PATH of bin, then the system.
func runHookAt(t *testing.T, hook, dir, bin string, env ...string) error {
	t.Helper()
	script, err := filepath.Abs(hook)
	require.NoError(t, err)
	msg := filepath.Join(t.TempDir(), "MSG")
	require.NoError(t, os.WriteFile(msg, []byte("subject\n"), 0o644))
	cmd := exec.Command("/bin/sh", script, msg)
	cmd.Dir = dir
	cmd.Env = append([]string{"HOME=" + t.TempDir(), "PATH=" + bin + ":/usr/bin:/bin"}, env...)
	return cmd.Run()
}

// In a dormant repository the hooks exit before they run mktemp or git: a git
// that fails and a mktemp that fails cannot block the commit.
func TestHooksDormantRepositoryRunsNoProbe(t *testing.T) {
	for _, hook := range []string{"pre-commit.sh", "commit-msg.sh"} {
		t.Run(hook, func(t *testing.T) {
			top := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(top, ".git"), 0o755))
			bin := fakeGitDir(t, "echo 'fatal: detected dubious ownership in repository at /x' >&2\nexit 128\n")
			shimMktemp(t, bin, "exit 1\n")
			require.NoError(t, runHookAt(t, hook, top, bin), "a dormant repository is not blocked by a probe failure")
		})
	}
}

// Where the marker is there, or the hook is not at a top level, the full
// resolution still decides: a failing git blocks, and an enabled repository
// reached from a subdirectory still seals.
func TestHooksStillResolveWhenEnabledOrNotAtTheTop(t *testing.T) {
	failing := "echo 'fatal: detected dubious ownership in repository at /x' >&2\nexit 128\n"
	for _, hook := range []string{"pre-commit.sh", "commit-msg.sh"} {
		t.Run(hook+"/marker present at the top", func(t *testing.T) {
			top := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(top, ".git"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(top, ".punt-labs", "ethos"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(top, ".punt-labs", "ethos", "enabled"), nil, 0o644))
			err := runHookAt(t, hook, top, fakeGitDir(t, failing))
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee)
			assert.Equal(t, 1, ee.ExitCode())
		})
		t.Run(hook+"/no .git here", func(t *testing.T) {
			top := t.TempDir()
			sub := filepath.Join(top, "sub")
			require.NoError(t, os.Mkdir(sub, 0o755))
			err := runHookAt(t, hook, sub, fakeGitDir(t, failing))
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee)
			assert.Equal(t, 1, ee.ExitCode(), "without its own .git the directory is not known to be a top level")
		})
	}
	t.Run("pre-commit seals from a subdirectory of an enabled repository", func(t *testing.T) {
		top := t.TempDir()
		sub := filepath.Join(top, "sub")
		require.NoError(t, os.Mkdir(sub, 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(top, ".punt-labs", "ethos"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(top, ".punt-labs", "ethos", "enabled"), nil, 0o644))
		bin := fakeGitDir(t, "echo \"$REPO\"\n")
		sealed := filepath.Join(t.TempDir(), "sealed")
		require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte("#!/bin/sh\ntouch '"+sealed+"'\n"), 0o755))
		require.NoError(t, runHookAt(t, "pre-commit.sh", sub, bin, "REPO="+top))
		assert.FileExists(t, sealed)
	})
}
