//go:build linux || darwin

package hooks

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A git that fails `rev-parse --show-toplevel` (dubious ownership, a corrupt
// .git) must not make the hooks skip silently with the host's status. Each
// prints git's own error to stderr and exits 1, so the commit is blocked and
// the operator sees why.
func TestHooksFailLoudWhenRevParseFails(t *testing.T) {
	for _, hook := range []string{"pre-commit.sh", "commit-msg.sh"} {
		t.Run(hook, func(t *testing.T) {
			bin := t.TempDir()
			fake := "#!/bin/sh\necho 'fatal: detected dubious ownership in repository at /x' >&2\nexit 128\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755))
			msg := filepath.Join(t.TempDir(), "MSG")
			require.NoError(t, os.WriteFile(msg, []byte("subject\n"), 0o644))

			cmd := exec.Command("/bin/sh", hook, msg)
			cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + bin + ":/usr/bin:/bin"}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, "the hook must fail, not exit with the host status 0")
			assert.Equal(t, 1, ee.ExitCode())
			assert.Contains(t, stderr.String(), "dubious ownership", "git's own error is shown")
			assert.Contains(t, stderr.String(), hook[:len(hook)-3], "the hook names itself")
		})
	}
}

// fakeGit writes a git script into a new directory and returns it, for PATH.
func fakeGitDir(t *testing.T, body string) string {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+body), 0o755))
	return bin
}

// A rev-parse that succeeds but writes to stderr (a warning, GIT_TRACE) must
// not be read as a failure or have its noise folded into the repository root:
// the hook still seals.
func TestPreCommitSealsDespiteRevParseStderrNoise(t *testing.T) {
	for name, body := range map[string]string{
		"a warning": `[ "$1" = rev-parse ] && { echo "warning: something harmless" >&2; echo "$REPO"; exit 0; }` + "\nexit 0\n",
		"GIT_TRACE": `[ "$1" = rev-parse ] && { echo "trace: built-in: git rev-parse --show-toplevel" >&2; echo "$REPO"; exit 0; }` + "\nexit 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), nil, 0o644))
			bin := fakeGitDir(t, body)
			marker := filepath.Join(t.TempDir(), "sealed")
			require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755))

			cmd := exec.Command("/bin/sh", "pre-commit.sh")
			cmd.Env = []string{"HOME=" + t.TempDir(), "REPO=" + repo, "PATH=" + bin + ":/usr/bin:/bin"}
			require.NoError(t, cmd.Run())
			assert.FileExists(t, marker, "the seal ran")
		})
	}
}

// When git interpret-trailers fails, commit-msg prints git's error and exits
// 1: the trailer is never silently replaced by a plain append.
func TestCommitMsgInterpretTrailersFailureBlocks(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), nil, 0o644))
	bin := fakeGitDir(t, `case "$1" in
rev-parse) echo "$REPO"; exit 0;;
interpret-trailers) echo "fatal: interpret-trailers exploded" >&2; exit 128;;
esac
exit 0
`)
	msg := filepath.Join(t.TempDir(), "MSG")
	require.NoError(t, os.WriteFile(msg, []byte("subject\n"), 0o644))

	cmd := exec.Command("/bin/sh", "commit-msg.sh", msg)
	cmd.Env = []string{"HOME=" + t.TempDir(), "REPO=" + repo, "MISSION_ID=m-2026-01-01-001", "PATH=" + bin + ":/usr/bin:/bin"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 1, ee.ExitCode())
	assert.Contains(t, stderr.String(), "interpret-trailers exploded")
	got, rerr := os.ReadFile(msg)
	require.NoError(t, rerr)
	assert.Equal(t, "subject\n", string(got), "no trailer was appended behind git's back")
}
