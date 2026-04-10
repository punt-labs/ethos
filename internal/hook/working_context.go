package hook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gitTimeout bounds all git subprocess calls in this file.
const gitTimeout = 2 * time.Second

// BuildWorkingContext returns a markdown section with the current git
// working state. Returns empty string if not in a git repo or if git
// is not available. Never errors -- this is advisory context, not a gate.
func BuildWorkingContext() string {
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}

	// Verify we are inside a git repo.
	if _, err := runGit("rev-parse", "--git-dir"); err != nil {
		return ""
	}

	branch := currentBranch()
	if branch == "" {
		return ""
	}

	var lines []string
	lines = append(lines, "## Working Context")
	lines = append(lines, "")
	lines = append(lines, "Branch: "+branch)

	dirty, paths, ok := uncommittedChanges()
	if ok {
		lines = append(lines, fmt.Sprintf("Uncommitted changes: %d", dirty))
		for _, p := range paths {
			lines = append(lines, "  "+p)
		}
	}

	if n, ok := unpushedCommits(); ok {
		lines = append(lines, fmt.Sprintf("Unpushed commits: %d", n))
	}

	return strings.Join(lines, "\n")
}

// runGit runs a git command with a gitTimeout deadline. Returns trimmed
// stdout on success, or the error on failure.
func runGit(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// currentBranch returns the current branch name, or the short SHA if
// HEAD is detached. Returns "" on error.
func currentBranch() string {
	branch, err := runGit("branch", "--show-current")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: git branch --show-current: %v\n", err)
		return ""
	}
	if branch != "" {
		return branch
	}
	// Detached HEAD -- use short SHA.
	sha, err := runGit("rev-parse", "--short", "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: git rev-parse --short HEAD: %v\n", err)
		return ""
	}
	return sha
}

// uncommittedChanges returns the count of dirty files, up to 20 file
// paths, and whether git status succeeded. The bool is false on error,
// so the caller can omit the line rather than report a misleading 0.
func uncommittedChanges() (int, []string, bool) {
	// Run git directly instead of runGit to preserve leading spaces.
	// Porcelain lines start with two status chars (e.g. " M") and
	// TrimSpace would strip the leading space, corrupting the parse.
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	raw, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: git status --porcelain: %v\n", err)
		return 0, nil, false
	}

	out := strings.TrimRight(string(raw), "\n")
	if out == "" {
		return 0, nil, true
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
	return total, paths, true
}

// unpushedCommits returns the number of commits ahead of the upstream
// tracking branch. The bool reports whether there are any unpushed
// commits to display; it is false when the upstream is missing or when
// the branch is fully pushed (both suppress the output line).
func unpushedCommits() (int, bool) {
	out, err := runGit("rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, false
	}
	if n == 0 {
		return 0, false
	}
	return n, true
}
