package autorepair

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

const stepGitignore = "gitignore"

func init() { Register(Step{Name: stepGitignore, Scope: ScopeRepository, Run: runGitignore}) }

// runGitignore replaces the former managed .gitignore block (v4.1-v4.20) with
// the single machine-local rule and always ensures that rule. Git hooks are
// refreshed first: a stale seal section can block every commit, and it must
// not wait on a .gitignore conversion that refuses (ethos-fdmx). Items counts
// .gitignore files written.
//
// A line that ignores the sessions record directory is deleted: the sessions
// records are tracked, the line is matched exactly, and every other byte of the
// file stays. A file ethos cannot rewrite leaves a residual with the cause.
func runGitignore(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if ctx.Err() != nil {
		out.Deferred = true
		return out, nil
	}
	// refreshHookSections logs its own failure. A failed refresh must not stop
	// the .gitignore conversion, but the hooks it left in place are residuals
	// of this step too.
	hooks, _ := refreshHookSections(ctx, env, stepGitignore)
	out.Residuals = append(out.Residuals, hooks.Residuals...)
	checkout, err := env.CheckoutRoot()
	if err != nil {
		return out, fmt.Errorf("locating the checkout for the .gitignore repair: %w", err)
	}
	store, err := env.StoreRoot()
	if err != nil {
		return out, fmt.Errorf("locating the store for the .gitignore repair: %w", err)
	}
	roots := []string{checkout}
	if store != "" && store != checkout {
		roots = append(roots, store)
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		path := filepath.Join(root, ".gitignore")
		changed, kept, err := enable.RepairGitignore(root, env.Policy)
		for _, k := range kept {
			out.Residuals = append(out.Residuals, actionableResidual(stepGitignore, k.Pattern+": "+relTo(root, k.Path),
				"the pattern still ignores another tool's local file; left in place so the file stays untracked",
				"clears when the pattern "+k.Pattern+" is no longer in "+relTo(root, path)))
		}
		if changed {
			out.Items++
			env.Log.Logf(stepGitignore, "rewrite", []string{path}, err)
		}
		if err != nil {
			return out, fmt.Errorf("repairing %s: %w", path, err)
		}
		removed, err := deleteSessionsIgnore(path)
		if err != nil {
			env.Log.Logf(stepGitignore, "delete-sessions-ignore", []string{path}, err)
			out.Residuals = append(out.Residuals, actionableResidual(stepGitignore, relTo(root, path)+": sessions ignore",
				fmt.Sprintf("%s ignores the sessions record directory, which holds tracked records, and ethos could not rewrite it: %v", relTo(root, path), redact.Prose(err.Error())),
				"clears when "+relTo(root, path)+" no longer ignores .punt-labs/ethos/sessions"))
			continue
		}
		if removed > 0 {
			out.Items++
			env.Log.Logf(stepGitignore, "delete-sessions-ignore", []string{path}, nil)
		}
	}
	return out, nil
}

// isSessionsIgnore reports whether line, without its line ending, is one of
// the three exact spellings that ignore the tracked sessions record directory.
func isSessionsIgnore(line string) bool {
	switch strings.TrimRight(line, "\r\n") {
	case ".punt-labs/ethos/sessions", ".punt-labs/ethos/sessions/", ".punt-labs/ethos/sessions/**":
		return true
	}
	return false
}

// deleteSessionsIgnore removes every exact sessions-ignore line from the
// .gitignore at path and returns how many it removed. Every other byte is kept:
// the file is rewritten whole, through a staging file in its own directory, with
// its mode. An absent file has nothing to remove; a file that is not a regular
// file is not ethos's to rewrite.
func deleteSessionsIgnore(path string) (int, error) {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", filepath.Base(path), err)
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", filepath.Base(path), err)
	}
	var kept strings.Builder
	removed := 0
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if isSessionsIgnore(line) {
			removed++
			continue
		}
		kept.WriteString(line)
	}
	if removed == 0 {
		return 0, nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), layout.DotTempPattern(filepath.Base(path)))
	if err != nil {
		return 0, fmt.Errorf("staging %s: %w", filepath.Base(path), err)
	}
	tmp := f.Name()
	_, werr := f.WriteString(kept.String())
	cerr := f.Close()
	if err := firstErr(werr, cerr, os.Chmod(tmp, fi.Mode().Perm())); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("writing %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("installing %s: %w", filepath.Base(path), err)
	}
	return removed, nil
}

// relTo is path relative to root, so a residual names the file without the
// machine's directory layout; the full path goes to the log. A path outside
// root is reduced to its base name.
func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}
