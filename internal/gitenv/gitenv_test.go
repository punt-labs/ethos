package gitenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsed(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	t.Setenv("GIT_COMMON_DIR", "/elsewhere/.git")
	t.Setenv("GIT_CEILING_DIRECTORIES", "/ceiling")

	env := Parsed()
	assert.Contains(t, env, "LC_ALL=C")
	assert.Contains(t, env, "LANGUAGE=")
	assert.Contains(t, env, "GIT_CEILING_DIRECTORIES=/ceiling", "a ceiling is the caller's to keep")
	for _, kv := range env {
		for _, gone := range []string{"LC_ALL=de", "LANGUAGE=de", "GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR="} {
			assert.False(t, strings.HasPrefix(kv, gone), "kept %s", kv)
		}
	}
}

// A git that does not answer is stopped after Timeout and reported as
// ErrTimeout naming git, its arguments and the directory: never a hang.
func TestOutputTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script git")
	}
	restore := SetTimeout(200 * time.Millisecond)
	t.Cleanup(func() { SetTimeout(restore) })
	dir := t.TempDir()
	git := filepath.Join(dir, "git")
	require.NoError(t, os.WriteFile(git, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755))
	start := time.Now()
	_, err := Output(dir, git, "rev-parse", "--show-toplevel")
	require.ErrorIs(t, err, ErrTimeout)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Contains(t, err.Error(), "200ms")
	assert.Contains(t, err.Error(), git)
	assert.Contains(t, err.Error(), dir)
	assert.Contains(t, err.Error(), "rev-parse --show-toplevel")
}

// Output runs git -C dir in the Parsed environment and returns its stdout;
// a failing git's stderr is left on the *exec.ExitError.
func TestOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script git")
	}
	dir := t.TempDir()
	git := filepath.Join(dir, "git")
	require.NoError(t, os.WriteFile(git, []byte("#!/bin/sh\necho \"$@ $LC_ALL\"\n[ \"$3\" = fail ] && { echo oops >&2; exit 3; }\nexit 0\n"), 0o755))
	out, err := Output(dir, git, "ok")
	require.NoError(t, err)
	assert.Equal(t, "-C "+dir+" ok C\n", string(out))

	out, err = Output("", git, "--version")
	require.NoError(t, err)
	assert.Equal(t, "--version C\n", string(out))

	_, err = Output(dir, git, "fail")
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, "oops\n", string(ee.Stderr))
}

// Production waits 10s for git; a test that shortens the deadline gets the
// previous value back to restore.
func TestTimeoutDefault(t *testing.T) {
	assert.Equal(t, 10*time.Second, Timeout)
	prev := SetTimeout(time.Millisecond)
	assert.Equal(t, Timeout, prev, "the deadline starts at Timeout")
	assert.Equal(t, time.Millisecond, SetTimeout(prev))
}
