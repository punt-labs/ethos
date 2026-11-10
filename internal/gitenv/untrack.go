package gitenv

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// ErrIndexBusy reports that another git process holds the index lock.
var ErrIndexBusy = errors.New("git index is locked by another process")

// untrackBatch bounds the paths given to one git invocation.
const untrackBatch = 256

// indexLocked is git's own line when another process holds the index lock.
var indexLocked = regexp.MustCompile(`Unable to create '[^']*index\.lock': File exists`)

// UntrackPaths stages the removal of paths, relative to the checkout at repo,
// from the actual committing index (ActualIndexEnv). Files stay on disk. A held
// index.lock yields an error wrapping ErrIndexBusy so a caller can try later.
func UntrackPaths(ctx context.Context, repo string, paths []string) error {
	env, err := ActualIndexEnv()
	if err != nil {
		return fmt.Errorf("selecting index to untrack from: %w", err)
	}
	for len(paths) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(len(paths), untrackBatch)
		if err := removeCached(repo, env, paths[:n]); err != nil {
			if indexLocked.MatchString(err.Error()) {
				return fmt.Errorf("untracking %d path(s): %w", n, errors.Join(ErrIndexBusy, err))
			}
			return fmt.Errorf("untracking %d path(s): %w", n, err)
		}
		paths = paths[n:]
	}
	return nil
}

// removeCached stages the removal of paths from the index env selects. Paths
// are literal, not pathspecs: a file named audit-*.jsonl must not take its
// siblings with it. -f is safe because --cached never touches the work tree,
// and without it git refuses a partly staged file and tells the user to retry.
func removeCached(dir string, env []string, paths []string) error {
	args := append([]string{"rm", "--cached", "-f", "-q", "--"}, paths...)
	_, _, err := Read(Spec{Git: "git", Dir: dir, Env: append(append([]string(nil), env...), "GIT_LITERAL_PATHSPECS=1"), Args: args}, 0)
	return err
}
