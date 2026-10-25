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

// Parsed drops every variable git lists in `git rev-parse --local-env-vars`
// (the repository-local ones: GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, ...) so a
// git run from a hook answers for the directory it is given, not for the
// repository the hook's parent git was working on.
func TestLocalEnvVarsMatchGit(t *testing.T) {
	out, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	for _, name := range strings.Fields(string(out)) {
		assert.Contains(t, LocalEnvVars, name, "git lists %s as repository-local; gitenv must clear it", name)
	}
	assert.Contains(t, LocalEnvVars, "GIT_NAMESPACE")
	for _, name := range LocalEnvVars {
		t.Setenv(name, "/victim")
	}
	for _, kv := range Parsed() {
		name, _, _ := strings.Cut(kv, "=")
		assert.NotContains(t, LocalEnvVars, name, "Parsed kept %s", kv)
	}
}

// On a timeout git gets SIGTERM first, so it can remove its index.lock, and
// SIGKILL only after a grace period.
func TestTimeoutTermsBeforeKill(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "index.lock")
	term := fakeGitScript(t, "touch '"+lock+"'\ntrap 'rm -f \""+lock+"\"; kill $!; exit 0' TERM\nsleep 60 &\nwait\n")
	testhelpersTimeout(t, 300*time.Millisecond)
	_, err := OutputEnv(dir, term, nil, "add")
	require.ErrorIs(t, err, ErrTimeout)
	assert.NoFileExists(t, lock, "git was TERMed and removed its lock")

	// A git that ignores TERM is killed after the grace.
	stubborn := fakeGitScript(t, "trap '' TERM\nsleep 60 &\nwait\n")
	start := time.Now()
	_, err = OutputEnv(dir, stubborn, nil, "add")
	require.ErrorIs(t, err, ErrTimeout)
	assert.Less(t, time.Since(start), 10*time.Second)
}

// Read classifies a failed git. The caller names what "absent" means; every
// other failure is an error carrying git's stderr.
func TestRead(t *testing.T) {
	script := func(body string) string { return fakeGitScript(t, body) }
	cases := map[string]struct {
		body   string
		absent Absent
		want   string // "ok", "absent" or "error"
		stderr string
	}{
		"ok":                   {"echo v\n", AbsentUnset, "ok", ""},
		"unset key":            {"exit 1\n", AbsentUnset, "absent", ""},
		"exit 1 with stderr":   {"echo boom >&2; exit 1\n", AbsentUnset, "error", "boom"},
		"exit 1 not asked for": {"exit 1\n", AbsentNoRemote, "error", ""},
		"no such remote":       {"echo 'error: No such remote' >&2; exit 2\n", AbsentNoRemote, "absent", ""},
		"exit 2 not asked for": {"exit 2\n", AbsentUnset, "error", ""},
		"not a repository":     {"echo 'fatal: not a git repository (or any of the parent directories): .git' >&2; exit 128\n", AbsentNotRepo, "absent", ""},
		"not a repo not asked": {"echo 'fatal: not a git repository' >&2; exit 128\n", AbsentUnset, "error", "not a git repository"},
		"other fatal":          {"echo 'fatal: dubious ownership' >&2; exit 128\n", AbsentNotRepo, "error", "dubious ownership"},
		"several rules":        {"exit 2\n", AbsentUnset | AbsentNoRemote | AbsentNotRepo, "absent", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, absent, err := Read(Spec{Git: script(c.body)}, c.absent)
			switch c.want {
			case "ok":
				require.NoError(t, err)
				assert.False(t, absent)
				assert.Equal(t, "v\n", string(out))
			case "absent":
				require.NoError(t, err)
				assert.True(t, absent)
			default:
				require.Error(t, err)
				assert.False(t, absent)
				assert.Contains(t, err.Error(), c.stderr)
				var ee *exec.ExitError
				assert.ErrorAs(t, err, &ee)
			}
		})
	}

	t.Run("git not installed is an error", func(t *testing.T) {
		_, absent, err := Read(Spec{Git: "ethos-test-no-such-git"}, AbsentUnset|AbsentNoRemote|AbsentNotRepo)
		require.Error(t, err)
		assert.False(t, absent)
		assert.ErrorIs(t, err, exec.ErrNotFound)
	})
	t.Run("timeout is an error", func(t *testing.T) {
		testhelpersTimeout(t, 150*time.Millisecond)
		_, absent, err := Read(Spec{Git: script("exec sleep 60\n")}, AbsentUnset|AbsentNoRemote|AbsentNotRepo)
		assert.ErrorIs(t, err, ErrTimeout)
		assert.False(t, absent)
	})
}
