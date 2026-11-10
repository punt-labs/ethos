package gitenv

import (
	"fmt"
	"strings"
)

// UnstageUnder removes every path under prefix from the index of the repository
// at dir and returns how many it removed. It covers both newly staged paths and
// paths HEAD already tracks, so the next commit deletes the latter. Files stay on
// disk. env selects the index, normally ActualIndexEnv, so a partial commit's
// private index is the one edited. prefix is a directory path relative to dir.
// A git failure is returned with git's own message.
func UnstageUnder(dir string, env []string, prefix string) (int, error) {
	out, _, err := Read(Spec{Git: "git", Dir: dir, Env: env, Args: []string{"ls-files", "-z", "--", prefix}}, 0)
	if err != nil {
		return 0, fmt.Errorf("listing index paths under %s: %w", prefix, err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return 0, nil
	}
	if err := removeCached(dir, env, paths); err != nil {
		return 0, fmt.Errorf("unstaging %d path(s) under %s: %w", len(paths), prefix, err)
	}
	return len(paths), nil
}
