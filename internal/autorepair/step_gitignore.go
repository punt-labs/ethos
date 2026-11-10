package autorepair

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/enable"
)

const stepGitignore = "gitignore"

func init() { Register(Step{Name: stepGitignore, Scope: ScopeRepository, Run: runGitignore}) }

// runGitignore replaces the former managed .gitignore block (v4.1-v4.20) with
// the single machine-local rule and always ensures that rule. Git hooks are
// refreshed first: a stale seal section can block every commit, and it must
// not wait on a .gitignore conversion that refuses (ethos-fdmx). Items counts
// .gitignore files written.
//
// A line outside the former block that ignores the sessions directory is the
// user's; it is left alone and recorded as a residual.
func runGitignore(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if ctx.Err() != nil {
		out.Deferred = true
		return out, nil
	}
	if _, err := refreshHookSections(ctx, env, stepGitignore); err != nil {
		env.Log.Logf(stepGitignore, "refresh-hooks", nil, err)
	}
	roots := []string{env.Roots.Checkout}
	if env.Roots.Store != "" && env.Roots.Store != env.Roots.Checkout {
		roots = append(roots, env.Roots.Store)
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		path := filepath.Join(root, ".gitignore")
		changed, err := enable.RepairGitignore(root, env.Policy)
		if changed {
			out.Items++
			env.Log.Logf(stepGitignore, "rewrite", []string{path}, err)
		}
		if err != nil {
			return out, fmt.Errorf("repairing %s: %w", path, err)
		}
		res, err := sessionsIgnoreResiduals(path)
		if err != nil {
			return out, err
		}
		out.Residuals = append(out.Residuals, res...)
	}
	return out, nil
}

// sessionsIgnoreResiduals names each line of the .gitignore at path that
// ignores .punt-labs/ethos/sessions, which holds tracked records.
func sessionsIgnoreResiduals(path string) ([]Residual, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var out []Residual
	for _, line := range strings.Split(string(b), "\n") {
		text := strings.TrimRight(line, "\r")
		switch text {
		case ".punt-labs/ethos/sessions", ".punt-labs/ethos/sessions/", ".punt-labs/ethos/sessions/**":
			out = append(out, Residual{
				Step:   stepGitignore,
				ID:     path + ": " + text,
				Reason: "a user rule ignores the sessions record directory; left in place",
			})
		}
	}
	return out, nil
}
