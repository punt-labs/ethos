package autorepair

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	if err := env.Roots.CheckoutErr; err != nil {
		return Outcome{}, fmt.Errorf("locating the checkout for untrack-local: %w", err)
	}
	if !env.InRepository() || env.Roots.Checkout == "" {
		return Outcome{}, nil
	}
	checkout := env.Roots.Checkout
	paths, residuals, deferred, err := untrackCandidates(env)
	if err != nil {
		return Outcome{}, fmt.Errorf("listing tracked local paths in %s: %w", checkout, err)
	}
	out := Outcome{Residuals: residuals, Deferred: deferred}
	if len(paths) == 0 {
		return out, nil
	}
	err = gitenv.UntrackPaths(ctx, checkout, paths)
	if errors.Is(err, gitenv.ErrIndexBusy) {
		logStep(env, "untrack-local", "deferred: index busy", nil, err)
		out.Deferred = true
		return out, nil
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
// the index, sorted. A path whose content could be read from neither the disk
// nor the index is a temporary condition: nothing proves it obsolete, so it
// stays tracked and the step reports deferred. A path whose publication status
// is permanently undecidable is a Residual; its reason carries only the
// repo-relative path and a fixed cause, the error goes to the redacting log.
func untrackCandidates(env Env) (paths []string, residuals []Residual, deferred bool, err error) {
	checkout := env.Roots.Checkout
	genv, err := gitenv.ActualIndexEnv()
	if err != nil {
		return nil, nil, false, err
	}
	out, _, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: checkout, Env: genv,
		Args: []string{"ls-files", "-z", "--", ".punt-labs/"}}, 0)
	if err != nil {
		return nil, nil, false, err
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		blob := func() ([]byte, error) {
			// A non-blob entry (gitlink, mode 160000) has no content: reading
			// it fails the same way on every run, so it is not transient.
			ls, _, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: checkout, Env: genv,
				Args: []string{"ls-files", "-s", "-z", "--", rel}}, 0)
			if err != nil {
				return nil, err
			}
			if !strings.HasPrefix(string(ls), "100") && !strings.HasPrefix(string(ls), "120") {
				return nil, errNotFile
			}
			b, _, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: checkout, Env: genv,
				Args: []string{"cat-file", "blob", ":0:" + rel}}, 0)
			return b, err
		}
		untrack, err := shouldUntrack(env.Policy, rel, blob)
		if err != nil {
			logStep(env, "untrack-local", "undecided", []string{rel}, err)
			if errors.Is(err, errNotFile) {
				residuals = append(residuals, Residual{Step: "untrack-local", ID: rel, Reason: "tracked entry is not a file: " + rel})
				continue
			}
			if errors.Is(err, repair.ErrNotRegular) {
				residuals = append(residuals, Residual{Step: "untrack-local", ID: rel, Reason: "tracked entry is not a regular file: " + rel})
				continue
			}
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errUnreadable) {
				deferred = true
				continue
			}
			residuals = append(residuals, Residual{Step: "untrack-local", ID: rel, Reason: "publication status unreadable: " + rel})
			continue
		}
		if untrack {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	return paths, residuals, deferred, nil
}

// errNotFile marks an index entry that is not a blob (a gitlink): permanent.
var errNotFile = errors.New("tracked entry is not a file")

// errUnreadable marks a tracked file readable from neither the disk nor the
// index: possibly a transient condition, so the step defers.
var errUnreadable = errors.New("content unreadable from disk and index")

// shouldUntrack decides for one tracked path. Record-tree trace is untracked
// exactly when repair.Obsolete says so; the answer is returned unchanged, so a
// published reflections aggregate, a session diagnostic, a full-consent log and
// a current mission's chunks stay tracked.
func shouldUntrack(p layout.Policy, rel string, blob func() ([]byte, error)) (bool, error) {
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
		obsolete, err := repair.Obsolete(p, p.CheckoutRecordRoot(), strings.TrimPrefix(rel, recordTree), func() ([]byte, error) {
			b, err := blob()
			if errors.Is(err, errNotFile) {
				return nil, err
			}
			if err != nil {
				return nil, fmt.Errorf("%w: %w", errUnreadable, err)
			}
			return b, nil
		})
		return obsolete, err
	}
	return layout.MisplacedLocal(rel), nil
}
