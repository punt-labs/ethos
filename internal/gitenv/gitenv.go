// Package gitenv runs the git commands whose output or messages ethos parses:
// in a fixed environment, and bounded by one timeout.
package gitenv

import (
	"context"
	"errors"
	"fmt"
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

// ErrTimeout is wrapped by the error Output returns when git did not answer
// within Timeout.
var ErrTimeout = errors.New("git did not answer")

// Output runs git with args — as `git -C dir args...` when dir is not "" —
// in the Parsed environment, and returns its stdout. A git that exits non-zero
// returns its *exec.ExitError (stderr captured). A git still running after
// Timeout (or the bound SetTimeout set) is killed and reported as ErrTimeout naming git, the arguments and
// the directory.
func Output(dir, git string, args ...string) ([]byte, error) {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	limit := time.Duration(deadline.Load())
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, full...)
	cmd.Env = Parsed()
	cmd.WaitDelay = time.Second // a child holding stdout does not stall the kill
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		where := ""
		if dir != "" {
			where = " in " + dir
		}
		return nil, fmt.Errorf("%w: %s %s%s did not answer within %v; refusing rather than guessing",
			ErrTimeout, git, strings.Join(args, " "), where, limit)
	}
	return out, err
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
