package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func init() {
	Register(Step{Name: "untrack-local", Scope: ScopeRepository, Run: untrackLocal})
}

const recordTree = ".punt-labs/ethos/"

var recordFull = regexp.MustCompile(`"record"\s*:\s*"full"`)

// untrackLocal stages the removal from the index of every tracked path that
// is machine-local: anything under .punt-labs/local/, any class-4 path, and
// record-tree trace that is not a full-mode publication. Files stay on disk.
func untrackLocal(ctx context.Context, env Env) (Outcome, error) {
	if !env.InRepository() || env.Roots.Checkout == "" {
		return Outcome{}, nil
	}
	checkout := env.Roots.Checkout
	paths, err := untrackCandidates(checkout)
	if err != nil {
		return Outcome{}, fmt.Errorf("listing tracked local paths in %s: %w", checkout, err)
	}
	if len(paths) == 0 {
		return Outcome{}, nil
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
	return Outcome{Items: len(paths)}, nil
}

func logStep(env Env, step, action string, paths []string, err error) {
	if env.Log != nil {
		env.Log.Logf(step, action, paths, err)
	}
}

// untrackCandidates lists the indexed paths under .punt-labs/ that belong off
// the index, sorted.
func untrackCandidates(checkout string) ([]string, error) {
	genv, err := gitenv.ActualIndexEnv()
	if err != nil {
		return nil, err
	}
	out, _, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: checkout, Env: genv,
		Args: []string{"ls-files", "-z", "--", ".punt-labs/"}}, 0)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" && shouldUntrack(checkout, rel) {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func shouldUntrack(checkout, rel string) bool {
	if layout.Match(".punt-labs/ethos/missions/*/artifacts/**", rel) {
		return false // agent-owned; no ethos code moves or exports them
	}
	if strings.HasPrefix(rel, layout.LocalPrefix) {
		return true
	}
	if class, _, ok := layout.Classify(rel); ok && class == layout.Never {
		return true
	}
	if !strings.HasPrefix(rel, recordTree) {
		return false
	}
	if layout.MisplacedLocal(rel) {
		return !publishedFull(checkout, rel)
	}
	if class, _, ok := layout.Classify(rel); ok && class == layout.Trace {
		return !publishedFull(checkout, rel)
	}
	return false
}

// publishedFull reports whether the tracked trace file rel is a current
// publication: a stream carrying record:full consent, or mission detail whose
// contract already holds round decisions or has an explicit export. Unreadable
// files are kept: nothing proves them obsolete.
func publishedFull(checkout, rel string) bool {
	path := filepath.Join(checkout, filepath.FromSlash(rel))
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	if strings.Contains(filepath.Base(rel), ".jsonl") && recordFull.Match(b) {
		return true
	}
	parts := strings.Split(rel, "/")
	// .punt-labs/ethos/missions/<id>/...
	if len(parts) >= 5 && parts[2] == "missions" && layout.Match(".punt-labs/ethos/missions/*/**", rel) {
		dir := filepath.Join(checkout, ".punt-labs", "ethos", "missions")
		id := parts[3]
		for _, suffix := range []string{".contract.yaml", ".result.yaml"} {
			if _, err := os.Stat(filepath.Join(dir, id+suffix)); err == nil {
				return true
			}
		}
		if base := filepath.Base(rel); base == "reflections.yaml" || base == "prompt.md" {
			c, err := os.ReadFile(filepath.Join(dir, id, "contract.yaml"))
			return err == nil && strings.Contains(string(c), "round_decisions")
		}
	}
	return false
}
