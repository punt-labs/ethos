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

// fakeGitScript writes an sh script named git and returns its path.
func fakeGitScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script git")
	}
	git := filepath.Join(t.TempDir(), "git")
	require.NoError(t, os.WriteFile(git, []byte("#!/bin/sh\n"+body), 0o755))
	return git
}

// OutputEnv keeps the caller's environment (here GIT_INDEX_FILE) and runs in
// dir without adding -C.
func TestOutputEnv(t *testing.T) {
	git := fakeGitScript(t, "echo \"$@|$GIT_INDEX_FILE|$PWD\"\n")
	dir := t.TempDir()
	t.Setenv("GIT_INDEX_FILE", "/the/index")
	out, err := OutputEnv(dir, git, nil, "status")
	require.NoError(t, err)
	assert.Contains(t, string(out), "status|/the/index|")
	assert.Contains(t, string(out), filepath.Base(dir))

	out, err = OutputEnv(dir, git, []string{"GIT_INDEX_FILE=/other"}, "status")
	require.NoError(t, err)
	assert.Contains(t, string(out), "status|/other|")
}

// A git that hangs under OutputEnv is ErrTimeout naming git, the arguments
// and the directory; with no dir the working directory is named.
func TestOutputEnvTimesOut(t *testing.T) {
	testhelpersTimeout(t, 200*time.Millisecond)
	git := fakeGitScript(t, "exec sleep 60\n")
	dir := t.TempDir()
	_, err := OutputEnv(dir, git, nil, "ls-files", "--stage")
	require.ErrorIs(t, err, ErrTimeout)
	for _, want := range []string{git, "ls-files --stage", dir} {
		assert.Contains(t, err.Error(), want)
	}
	cwd, werr := os.Getwd()
	require.NoError(t, werr)
	_, err = OutputEnv("", git, nil, "status")
	require.ErrorIs(t, err, ErrTimeout)
	assert.Contains(t, err.Error(), cwd)
}

// Run feeds Stdin, streams Stdout and Stderr to writers, and honours its own
// Limit instead of the shared deadline.
func TestRunStreamsAndLimit(t *testing.T) {
	git := fakeGitScript(t, "cat; echo err >&2\n")
	var so, se strings.Builder
	out, err := Run(Spec{Git: git, Stdin: strings.NewReader("in"), Stdout: &so, Stderr: &se})
	require.NoError(t, err)
	assert.Nil(t, out)
	assert.Equal(t, "in", so.String())
	assert.Equal(t, "err\n", se.String())

	hang := fakeGitScript(t, "exec sleep 60\n")
	_, err = Run(Spec{Git: hang, Limit: 150 * time.Millisecond, Stdout: &so})
	require.ErrorIs(t, err, ErrTimeout)
	assert.Contains(t, err.Error(), "150ms")
}

func testhelpersTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := SetTimeout(d)
	t.Cleanup(func() { SetTimeout(prev) })
}
