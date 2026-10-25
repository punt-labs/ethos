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
	cmd.WaitDelay = time.Second // a child holding stdout does not stall the kill
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

// dropped are the variables Parsed removes: the locale, which Parsed sets
// itself, and the repository locations, which would make git answer for the
// repository they name instead of discovering one from the directory it is
// given. A pre-commit hook runs with GIT_DIR exported; from a subdirectory,
// git would then call that subdirectory the top level.
var dropped = map[string]bool{
	"LC_ALL":         true,
	"LANGUAGE":       true,
	"GIT_DIR":        true,
	"GIT_WORK_TREE":  true,
	"GIT_COMMON_DIR": true,
}

// Parsed returns the current environment with git's messages in the C locale
// (LC_ALL=C, LANGUAGE empty) and no GIT_DIR, GIT_WORK_TREE or GIT_COMMON_DIR.
// ethos matches git's English messages ("not a git repository", "dubious
// ownership"); a translated message would match nothing.
func Parsed() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !dropped[name] {
			env = append(env, kv)
		}
	}
	return append(env, "LC_ALL=C", "LANGUAGE=")
}
