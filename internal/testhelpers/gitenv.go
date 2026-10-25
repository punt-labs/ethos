package testhelpers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

// Git test isolation.
//
// A git command run by a test must touch only the test's own temporary
// repository. Two things in the environment can send it elsewhere: a
// location variable (GIT_DIR and its kin), which makes `git init`, `git
// config` and `git commit` act on whatever repository it names, and the
// upward walk from a temporary directory, which in this org reaches the
// enclosing checkout because TMPDIR lives inside it. A reviewer once ran
// a package's tests with GIT_DIR exported; the helpers then set core.bare,
// user.email and an "init" commit in the real shared repository.
//
// IsolateGit, called from every TestMain whose package runs git, removes
// both hazards for the whole process. GitCommand removes them again at the
// moment a helper runs git, so a test that sets a location variable itself
// cannot redirect a helper either.

// gitLocationVars name the repository a git command acts on. They are
// stripped from every helper's environment.
var gitLocationVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CEILING_DIRECTORIES",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
}

// gitProcessPrefixes are the further variables IsolateGit removes from the
// test process: configuration injection and commit identity.
var gitProcessPrefixes = []string{"GIT_CONFIG", "GIT_AUTHOR_", "GIT_COMMITTER_"}

// isGitLocation reports whether the environment entry kv sets a location
// variable.
func isGitLocation(kv string) bool {
	name, _, _ := strings.Cut(kv, "=")
	for _, v := range gitLocationVars {
		if name == v {
			return true
		}
	}
	return false
}

// GitCeiling returns the directory git must not walk up into from a test's
// temporary directory: the temporary root itself.
func GitCeiling() string {
	return os.TempDir()
}

// IsolateGit prepares the test process for running git: it unsets every git
// location, configuration and identity variable and sets
// GIT_CEILING_DIRECTORIES to the temporary root, so a temporary directory
// never resolves to an enclosing repository. Call it from TestMain before
// m.Run.
func IsolateGit() error {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !isGitLocation(kv) && !hasAnyPrefix(name, gitProcessPrefixes) {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			return fmt.Errorf("unsetting %s: %w", name, err)
		}
	}
	if err := os.Setenv("GIT_CEILING_DIRECTORIES", GitCeiling()); err != nil {
		return fmt.Errorf("setting GIT_CEILING_DIRECTORIES: %w", err)
	}
	return nil
}

// MustIsolateGit is IsolateGit for TestMain: it exits on failure, since no
// test may run with git pointed at a repository it does not own.
func MustIsolateGit() {
	if err := IsolateGit(); err != nil {
		fmt.Fprintf(os.Stderr, "isolating git for tests: %v\n", err)
		os.Exit(2)
	}
}

// GitEnv returns the current environment without any git location variable
// and with GIT_CEILING_DIRECTORIES at the temporary root. It is read at call
// time, so a variable a test set with t.Setenv is stripped too.
func GitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !isGitLocation(kv) {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CEILING_DIRECTORIES="+GitCeiling())
}

// GitCommand is exec.Command("git", args...) with the GitEnv environment. A
// caller that adds variables appends to cmd.Env rather than replacing it.
func GitCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Env = GitEnv()
	return cmd
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// GitTimeout shortens the bound gitenv.Output applies to d for the rest of
// the test, so a test of a git that hangs does not wait gitenv.Timeout.
func GitTimeout(t testing.TB, d time.Duration) {
	t.Helper()
	prev := gitenv.SetTimeout(d)
	t.Cleanup(func() { gitenv.SetTimeout(prev) })
}

// HangingGit puts a git that never answers alone on PATH and shortens the git
// deadline to 200ms for the rest of the test, so a caller's timeout path runs
// without waiting gitenv.Timeout. It skips where there is no sh and sleep.
func HangingGit(t testing.TB) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("needs sh and sleep")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nexec " + sleep + " 60\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the hanging git: %v", err)
	}
	t.Setenv("PATH", dir)
	GitTimeout(t, 200*time.Millisecond)
}

// ShimGit puts a git first on PATH that runs the real git, except that a
// command whose first argument is in hang never answers; it shortens the git
// deadline to 200ms. A test uses it to make exactly one git question time out
// while every other works. The `-C dir` prefix is skipped when naming the
// command.
func ShimGit(t testing.TB, hang ...string) {
	t.Helper()
	real, err := exec.LookPath("git")
	sleep, serr := exec.LookPath("sleep")
	if err != nil || serr != nil || runtime.GOOS == "windows" {
		t.Skip("needs git, sh and sleep")
	}
	script := "#!/bin/sh\ncmd=\"$1\"\n[ \"$1\" = -C ] && cmd=\"$3\"\ncase \"$cmd\" in\n"
	for _, h := range hang {
		script += "  " + h + ") exec " + sleep + " 60 ;;\n"
	}
	script += "esac\nexec " + real + " \"$@\"\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the git shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	GitTimeout(t, 200*time.Millisecond)
}
