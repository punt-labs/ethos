package gitenv

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrIndexBusy reports that another git process holds the index lock.
var ErrIndexBusy = errors.New("git index is locked by another process")

// untrackBatch bounds the paths given to one git invocation.
const untrackBatch = 256

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
		args := append([]string{"rm", "--cached", "-q", "--"}, paths[:n]...)
		_, _, err := Read(Spec{Git: "git", Dir: repo, Env: env, Args: args}, 0)
		if err != nil {
			if strings.Contains(err.Error(), "index.lock") {
				return fmt.Errorf("untracking %d path(s): %w", n, errors.Join(ErrIndexBusy, err))
			}
			return fmt.Errorf("untracking %d path(s): %w", n, err)
		}
		paths = paths[n:]
	}
	return nil
}
