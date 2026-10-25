// Package gitenv runs the git commands whose output or messages ethos parses:
// in a fixed environment, and bounded by one timeout.
package gitenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Timeout bounds every git command ethos runs. git blocks forever on some
// layouts (it reads a FIFO commondir); a command that has not answered by
// then is an error, never a hang.
const Timeout = 10 * time.Second

// deadline is the bound Output applies: Timeout, unless a test shortened it.
var deadline atomic.Int64

func init() { deadline.Store(int64(Timeout)) }

// SetTimeout sets the bound Output applies and returns the previous one, for
// a test that must not wait Timeout for a git that hangs. Use it through
// testhelpers.GitTimeout, which restores the previous bound.
func SetTimeout(d time.Duration) time.Duration {
	return time.Duration(deadline.Swap(int64(d)))
}

// ErrTimeout is wrapped by the error Output, OutputEnv and Run return when
// git did not answer within its bound.
var ErrTimeout = errors.New("git did not answer")

// Output runs git with args — as `git -C dir args...` when dir is not "" —
// in the Parsed environment, and returns its stdout. A git that exits non-zero
// returns its *exec.ExitError (stderr captured). A git still running after
// Timeout (or the bound SetTimeout set) is killed and reported as ErrTimeout
// naming git, the arguments and the directory.
func Output(dir, git string, args ...string) ([]byte, error) {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	out, err := Run(Spec{Git: git, Args: full, Env: Parsed()})
	if errors.Is(err, ErrTimeout) {
		return nil, timeoutError(git, args, dir, time.Duration(deadline.Load()))
	}
	return out, err
}

// OutputEnv is Output for a caller that needs its own environment: git runs
// in dir (cmd.Dir; the working directory when "") with env, or the inherited
// environment when env is nil. It is bounded and reports a timeout as Output
// does.
func OutputEnv(dir, git string, env []string, args ...string) ([]byte, error) {
	return Run(Spec{Git: git, Dir: dir, Env: env, Args: args})
}

// Spec describes one bounded git command for Run.
type Spec struct {
	Git    string        // the git binary
	Dir    string        // cmd.Dir; "" is the working directory
	Env    []string      // nil inherits the process environment
	Args   []string      // arguments after git
	Stdin  io.Reader     // nil for none
	Stdout io.Writer     // nil: captured and returned
	Stderr io.Writer     // nil: captured on the *exec.ExitError
	Limit  time.Duration // 0: the shared deadline (Timeout, or SetTimeout's)
}

// Run executes s under its bound. It returns stdout when s.Stdout is nil. A
// git that exits non-zero returns its *exec.ExitError; one still running at
// the bound is killed and reported as ErrTimeout naming git, the arguments,
// the directory and the bound.
func Run(s Spec) ([]byte, error) {
	limit := s.Limit
	if limit == 0 {
		limit = time.Duration(deadline.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Git, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	cmd.Stdin = s.Stdin
	// On the deadline git gets SIGTERM, so it can remove its index.lock; after
	// the WaitDelay grace Go kills it. A child holding stdout does not stall
	// the kill either.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill() // no SIGTERM here (Windows)
		}
		return nil
	}
	cmd.WaitDelay = time.Second
	var out []byte
	var err error
	if s.Stdout == nil {
		cmd.Stderr = s.Stderr
		out, err = cmd.Output()
	} else {
		cmd.Stdout, cmd.Stderr = s.Stdout, s.Stderr
		err = cmd.Run()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return nil, timeoutError(s.Git, s.Args, s.Dir, limit)
	}
	return out, err
}

// timeoutError names the command and where it ran; an empty dir is reported
// as the working directory.
func timeoutError(git string, args []string, dir string, limit time.Duration) error {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	return fmt.Errorf("%w: %s %s in %s did not answer within %v; refusing rather than guessing",
		ErrTimeout, git, strings.Join(args, " "), dir, limit)
}

// LocalEnvVars are the repository-local variables Parsed removes: the ones
// `git rev-parse --local-env-vars` lists (a test compares them), plus
// GIT_NAMESPACE. Left set, they make git answer for the repository they name
// instead of discovering one from the directory it is given. A pre-commit
// hook runs with GIT_DIR and GIT_INDEX_FILE exported; from a subdirectory,
// git would then call that subdirectory the top level.
var LocalEnvVars = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CONFIG",
	"GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY",
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE",
	"GIT_GRAFT_FILE",
	"GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS",
	"GIT_REPLACE_REF_BASE",
	"GIT_PREFIX",
	"GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
	"GIT_NAMESPACE",
}

// dropped is LocalEnvVars plus the locale, which Parsed sets itself.
var dropped = func() map[string]bool {
	m := map[string]bool{"LC_ALL": true, "LANGUAGE": true}
	for _, v := range LocalEnvVars {
		m[v] = true
	}
	return m
}()

// isTrace reports whether name makes git write diagnostics to stderr:
// GIT_TRACE, GIT_TRACE_*, GIT_TRACE2, GIT_TRACE2_* and GIT_CURL_VERBOSE. A
// read classifies git's stderr, so trace noise would turn "key unset" (exit 1,
// empty stderr) into a failure.
func isTrace(name string) bool {
	return name == "GIT_CURL_VERBOSE" || strings.HasPrefix(name, "GIT_TRACE")
}

// NotARepoMessage is what git prints when it walked up and found no
// repository at all. It is NOT in the message for a .git git found and could
// not use (a gitfile naming a missing directory, a worktree whose main
// checkout is gone): that one says "not a git repository: <path>" and is a
// failure, not "outside a repository". One matcher, used by Read, the
// resolver and enable.
const NotARepoMessage = "not a git repository (or any"

// IsNotARepo reports whether git's stderr says it found no repository.
func IsNotARepo(stderr string) bool {
	return strings.Contains(stderr, NotARepoMessage)
}

// Parsed returns the current environment with git's messages in the C locale
// (LC_ALL=C, LANGUAGE empty) and none of LocalEnvVars. ethos matches git's
// English messages ("not a git repository", "dubious ownership"); a
// translated message would match nothing.
func Parsed() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !dropped[name] && !isTrace(name) {
			env = append(env, kv)
		}
	}
	return append(env, "LC_ALL=C", "LANGUAGE=")
}

// Inherited returns the current environment unchanged except that git's
// messages are in the C locale. A caller that needs its own GIT_DIR or
// GIT_INDEX_FILE (the seal, inside a pre-commit hook) and matches git's
// English messages uses it.
func Inherited() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name != "LC_ALL" && name != "LANGUAGE" && !isTrace(name) {
			env = append(env, kv)
		}
	}
	return append(env, "LC_ALL=C", "LANGUAGE=")
}

// Absent says which failed exits of a read mean "there is nothing there".
// Every other failure is an error: a git failure must never read as an absent
// value (an empty repo, a missing user, "not a gitlink").
type Absent int

const (
	// AbsentUnset: exit 1 with empty stderr — `git config` for an unset key.
	AbsentUnset Absent = 1 << iota
	// AbsentNoRemote: exit 2 — `git remote get-url` for a remote that does not exist.
	AbsentNoRemote
	// AbsentNotRepo: git's "not a git repository" message.
	AbsentNotRepo
)

// Read runs s and classifies the result. A success returns stdout. A failure
// that matches a rule in absent returns (nil, true, nil). Any other failure —
// a timeout, git not installed, another exit status, an unexpected message —
// returns an error carrying git's stderr, wrapping the *exec.ExitError (or
// ErrTimeout, or exec.ErrNotFound) for errors.Is and errors.As.
func Read(s Spec, absent Absent) (out []byte, isAbsent bool, err error) {
	out, err = Run(s)
	if err == nil {
		return out, false, nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return nil, false, fmt.Errorf("git %s: %w", strings.Join(s.Args, " "), err)
	}
	stderr := strings.TrimSpace(string(ee.Stderr))
	switch {
	case absent&AbsentUnset != 0 && ee.ExitCode() == 1 && stderr == "":
		return nil, true, nil
	case absent&AbsentNoRemote != 0 && ee.ExitCode() == 2:
		return nil, true, nil
	case absent&AbsentNotRepo != 0 && IsNotARepo(stderr):
		return nil, true, nil
	}
	return nil, false, fmt.Errorf("git %s: %w: %s", strings.Join(s.Args, " "), err, stderr)
}
