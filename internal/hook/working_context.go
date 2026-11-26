package hook

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

// gitTimeout bounds each git subprocess call in this file; a test shortens it.
var gitTimeout = 2 * time.Second

// BuildWorkingContext returns a markdown section with the git working state of
// checkout, the work tree the resolver located. It returns "" when there is no
// checkout (outside a repository) or git is not installed. Advisory context,
// not a gate, but a git that fails is said so in the section, never dropped:
// a session told nothing would read "no information" as "nothing to report".
func BuildWorkingContext(checkout string) string {
	if checkout == "" {
		return ""
	}
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}

	branch, berr := currentBranch(checkout)
	lines := []string{"## Working Context", ""}
	if berr != nil {
		lines = append(lines, "Branch: unknown ("+berr.Error()+")")
	} else {
		lines = append(lines, "Branch: "+branch)
	}

	// A git that did not answer once will not answer the next question either:
	// say so for each and do not wait again.
	hung := errors.Is(berr, gitenv.ErrTimeout)
	var dirty int
	var paths []string
	var serr error
	if hung {
		serr = berr
	} else {
		dirty, paths, serr = uncommittedChanges(checkout)
	}
	if serr != nil {
		lines = append(lines, "Uncommitted changes: unknown ("+serr.Error()+")")
	} else {
		lines = append(lines, fmt.Sprintf("Uncommitted changes: %d", dirty))
		for _, p := range paths {
			lines = append(lines, "  "+p)
		}
	}

	var n int
	var uerr error
	if hung || errors.Is(serr, gitenv.ErrTimeout) {
		uerr = berr
		if uerr == nil {
			uerr = serr
		}
	} else {
		n, uerr = unpushedCommits(checkout)
	}
	switch {
	case uerr != nil:
		lines = append(lines, "Unpushed commits: unknown ("+uerr.Error()+")")
	case n > 0:
		lines = append(lines, fmt.Sprintf("Unpushed commits: %d", n))
	}

	return strings.Join(lines, "\n")
}

// runGit runs a git command in checkout with a gitTimeout deadline, in the
// Parsed environment (the C locale, no GIT_DIR from a hook). It returns
// stdout, or an error that says what git said or that it did not answer.
func runGit(checkout string, args ...string) (string, error) {
	var stderr bytes.Buffer
	out, err := gitenv.Run(gitenv.Spec{Git: "git", Dir: checkout, Env: gitenv.Parsed(), Args: args, Limit: gitTimeout, Stderr: &stderr})
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" && !errors.Is(err, gitenv.ErrTimeout) {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// currentBranch returns the current branch name, or the short SHA if HEAD is
// detached.
func currentBranch(checkout string) (string, error) {
	out, err := runGit(checkout, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if branch := strings.TrimSpace(out); branch != "" {
		return branch, nil
	}
	// Detached HEAD -- use short SHA.
	sha, err := runGit(checkout, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// uncommittedChanges returns the count of dirty files, up to 20 file
// paths, and the error when git status failed, so the caller reports it
// rather than a misleading 0.
func uncommittedChanges(checkout string) (int, []string, error) {
	// runGit does not trim: porcelain lines start with two status chars
	// (e.g. " M") and TrimSpace would strip the leading space, corrupting the
	// parse.
	raw, err := runGit(checkout, "status", "--porcelain")
	if err != nil {
		return 0, nil, err
	}

	out := strings.TrimRight(raw, "\n")
	if out == "" {
		return 0, nil, nil
	}

	rawLines := strings.Split(out, "\n")
	total := len(rawLines)

	const maxShow = 20
	n := total
	if n > maxShow {
		n = maxShow
	}

	paths := make([]string, n)
	for i := 0; i < n; i++ {
		line := rawLines[i]
		// Porcelain format: XY <path> or XY <path> -> <path>.
		// The path starts at column 3.
		if len(line) > 3 {
			paths[i] = line[3:]
		} else {
			paths[i] = strings.TrimSpace(line)
		}
	}
	if total > maxShow {
		paths = append(paths, fmt.Sprintf("... and %d more", total-maxShow))
	}
	return total, paths, nil
}

// unpushedCommits returns the number of commits ahead of the upstream
// tracking branch. A branch with no upstream (or a detached HEAD, or no commit
// yet) has none to count: 0 and no error. Any other failure is the error.
func unpushedCommits(checkout string) (int, error) {
	out, err := runGit(checkout, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		if noUpstream(err) {
			return 0, nil
		}
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count printed %q, not a number", strings.TrimSpace(out))
	}
	return n, nil
}

// noUpstream reports whether err is git saying there is nothing to compare
// against (git runs in the C locale, so the English messages are the ones to
// match).
func noUpstream(err error) bool {
	if errors.Is(err, gitenv.ErrTimeout) {
		return false
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	msg := err.Error()
	for _, m := range []string{"no upstream configured", "does not point to a branch", "unknown revision or path", "ambiguous argument 'HEAD'", "bad revision 'HEAD'"} {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
