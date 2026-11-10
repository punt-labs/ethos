package autorepair

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/repair"
)

func init() {
	Register(Step{Name: "untrack-local", Scope: ScopeRepository, Run: untrackLocal})
}

const recordTree = ".punt-labs/ethos/"

// untrackLocal stages the removal from the index of every tracked path that
// is machine-local: anything under .punt-labs/local/, any class-4 path, and
// record-tree trace that repair.Inventory calls obsolete. Files stay on disk.
func untrackLocal(ctx context.Context, env Env) (Outcome, error) {
	if !env.InRepository() || env.Roots.Checkout == "" {
		return Outcome{}, nil
	}
	checkout := env.Roots.Checkout
	paths, residuals, err := untrackCandidates(env)
	if err != nil {
		return Outcome{}, fmt.Errorf("listing tracked local paths in %s: %w", checkout, err)
	}
	out := Outcome{Residuals: residuals}
	if len(paths) == 0 {
		return out, nil
	}
	err = gitenv.UntrackPaths(ctx, checkout, paths)
	if errors.Is(err, gitenv.ErrIndexBusy) {
		logStep(env, "untrack-local", "deferred: index busy", nil, err)
		return Outcome{Deferred: true}, nil
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("untracking local paths in %s: %w", checkout, err)
	}
	logStep(env, "untrack-local", "untracked", paths, nil)
	out.Items = len(paths)
	return out, nil
}

func logStep(env Env, step, action string, paths []string, err error) {
	if env.Log != nil {
		env.Log.Logf(step, action, paths, err)
	}
}

// untrackCandidates lists the indexed paths under .punt-labs/ that belong off
// the index, sorted, and a Residual for each path whose publication status
// could not be read: nothing proves those obsolete, so they stay tracked.
func untrackCandidates(env Env) ([]string, []Residual, error) {
	checkout := env.Roots.Checkout
	genv, err := gitenv.ActualIndexEnv()
	if err != nil {
		return nil, nil, err
	}
	out, _, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: checkout, Env: genv,
		Args: []string{"ls-files", "-z", "--", ".punt-labs/"}}, 0)
	if err != nil {
		return nil, nil, err
	}
	var paths []string
	var residuals []Residual
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		untrack, err := shouldUntrack(env.Policy, rel)
		if err != nil {
			residuals = append(residuals, Residual{Step: "untrack-local", ID: rel, Reason: err.Error()})
			continue
		}
		if untrack {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	return paths, residuals, nil
}

// shouldUntrack decides for one tracked path. Record-tree trace is untracked
// exactly when repair.Inventory would call it obsolete, so a published
// reflections aggregate, a session diagnostic and a current mission's chunks
// stay tracked.
func shouldUntrack(p layout.Policy, rel string) (bool, error) {
	if layout.Match(".punt-labs/ethos/missions/*/artifacts/**", rel) {
		return false, nil // agent-owned; no ethos code moves or exports them
	}
	if strings.HasPrefix(rel, layout.LocalPrefix) {
		return true, nil
	}
	if class, _, ok := layout.Classify(rel); ok && class == layout.Never {
		return true, nil
	}
	if !strings.HasPrefix(rel, recordTree) {
		return false, nil
	}
	class, _, classified := layout.Classify(rel)
	if classified && class == layout.Trace {
		obsolete, err := repair.Obsolete(p, p.CheckoutRecordRoot(), strings.TrimPrefix(rel, recordTree))
		if err != nil || obsolete {
			return obsolete, err
		}
		// A live session stream is never a publication, even in a directory
		// whose name repair cannot match to a session.
		return layout.Match(".punt-labs/ethos/sessions/*/audit.jsonl", rel), nil
	}
	return layout.MisplacedLocal(rel), nil
}
