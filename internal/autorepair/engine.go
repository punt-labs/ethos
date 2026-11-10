package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// BinaryVersion is recorded in the stamp and the log. The command sets it
// once, at process start, before any Ensure; nothing writes it afterwards,
// so the MCP server's concurrent tool calls only read it.
var BinaryVersion = "dev"

// Result is what one Ensure did. Items sums the conversions of the steps
// that finished; an errored, deferred or abandoned step's count is discarded
// because its work is not known to be complete. Residuals are the permanent
// conditions finished steps recorded; they never produce a line.
type Result struct {
	Items     int
	Repaired  bool
	Residuals []Residual
}

// Line is the one diagnostic line a repair earns, or "" when nothing was
// repaired.
func (r Result) Line() string {
	if !r.Repaired {
		return ""
	}
	return fmt.Sprintf("ethos: upgraded storage (%d items; see ~/.punt-labs/ethos/logs/repair.log)", r.Items)
}

// options is Ensure's dependency set; the tests replace the steps and the
// budgets.
type options struct {
	ethos        string
	now          func() time.Time
	stepBudget   time.Duration
	ensureBudget time.Duration
	lockWait     time.Duration
	steps        []Step
	order        []string
}

func (options) globalLockPath(global string) string { return globalLockPath(global) }

func defaultOptions() options {
	return options{
		ethos:        BinaryVersion,
		now:          time.Now,
		stepBudget:   StepBudget,
		ensureBudget: EnsureBudget,
		lockWait:     LockWait,
		steps:        Steps,
		order:        StepOrder,
	}
}

// Ensure brings the global store and, in a repository that has used ethos,
// the repository store to the current layout. Current stamps cost two stats
// and two small reads. Otherwise it runs the registered steps in StepOrder
// under the repair locks. A repair problem is logged and left for the next
// entry; it never fails the caller, so the error is always nil.
func Ensure(ctx context.Context, roots resolve.Located, global string) (Result, error) {
	return ensure(ctx, roots, global, defaultOptions())
}

// CheapPass runs the named steps now, whatever the stamps say, and never
// writes a stamp. It is for the per-event conversions DES-084 names: the
// hooks call it after Ensure, pre-commit with "live-tails", "untrack-local"
// and "hook-sections" (steps 6, 9 and 16) and session start with "sessions"
// (step 17). It takes the global repair lock without waiting and skips the
// pass when the lock is busy, builds the Env the way Ensure does from
// env.Roots and env.Global (policy, logger, clock), runs only the named
// steps that are registered, in StepOrder, logs under the lock, and returns
// Ensure's Result shape. A name no unit registers is skipped, not an error.
func CheapPass(ctx context.Context, names []string, env Env) (Result, error) {
	return cheapPass(ctx, names, env.Roots, env.Global, defaultOptions())
}

// target is one store Ensure converts: its stamp, what is already recorded,
// and what this run learns.
type target struct {
	path     string
	ensure   func(dir string) error
	needed   bool
	blocked  bool // a step errored, deferred, was abandoned or is not registered
	items    int
	residual []Residual
}

// refresh reads the stamp: a stat first, the file only when it is present.
func (t *target) refresh() {
	if _, err := os.Lstat(t.path); err != nil {
		t.needed = true
		return
	}
	s, err := ReadStamp(t.path)
	t.needed = err != nil || !s.Current()
}

// repositoryTarget is the repository store behind roots, or nil outside a
// repository and in a repository that never used ethos (no .punt-labs
// directory): Ensure leaves such a repository byte-identical, so a stray
// `ethos whoami` there creates nothing for `git add -A` to pick up.
func repositoryTarget(roots resolve.Located) (*target, layout.Policy) {
	if roots.Store == "" {
		return nil, layout.Policy{}
	}
	info, err := os.Stat(filepath.Join(roots.Store, ".punt-labs"))
	if err != nil || !info.IsDir() {
		return nil, layout.Policy{}
	}
	p, err := roots.Layout()
	if err != nil {
		return nil, layout.Policy{}
	}
	return &target{path: RepositoryStampPath(p), ensure: func(d string) error { return layout.EnsureLocalDir(p, d) }}, p
}

func ensure(ctx context.Context, roots resolve.Located, global string, o options) (res Result, err error) {
	if !supported || global == "" {
		return Result{}, nil
	}
	var log *FileLog
	defer func() {
		// Nothing in the repair may fail the command that triggered it.
		if p := recover(); p != nil {
			if log != nil {
				log.Logf("engine", "panic", nil, fmt.Errorf("%v", p))
			}
			res, err = Result{}, nil
		}
	}()
	g := &target{path: GlobalStampPath(global), ensure: mkdirPrivate}
	r, policy := repositoryTarget(roots)
	targets := []*target{g}
	if r != nil {
		targets = append(targets, r)
	}
	for _, t := range targets {
		t.refresh()
	}
	if !g.needed && (r == nil || !r.needed) {
		return Result{}, nil
	}

	release, lerr := acquire(o.globalLockPath(global), o.lockWait)
	if lerr != nil {
		// Busy, or the lock file cannot be made: the holder is converting, or
		// the next entry will. The caller continues.
		return Result{}, nil
	}
	defer release()
	for _, t := range targets {
		t.refresh() // another process may have finished while we waited
	}
	if !g.needed && (r == nil || !r.needed) {
		return Result{}, nil
	}

	log = newFileLog(global, roots.Store, roots.Checkout, o.ethos)
	if r != nil && r.needed {
		if rel, rerr := repositoryLock(policy, o.lockWait); rerr != nil {
			log.Logf("engine", "defer-repository", []string{roots.Store}, rerr)
			r.needed = false
		} else {
			defer rel()
		}
	}
	if !g.needed && (r == nil || !r.needed) {
		return Result{}, nil
	}

	env := Env{Roots: roots, Policy: policy, Global: global, Now: o.now(), Log: log}
	res = runSteps(ctx, env, o, log, g, r, nil)
	for _, t := range targets {
		if !t.needed || t.blocked {
			continue
		}
		s := Stamp{
			Version:    LayoutVersion,
			Ethos:      o.ethos,
			RepairedAt: env.Now.UTC().Format(time.RFC3339),
			Items:      t.items,
			Residual:   t.residual,
		}
		if werr := WriteStamp(t.path, s, t.ensure); werr != nil {
			log.Logf("engine", "write-stamp", []string{t.path}, werr)
		}
	}
	return res, nil
}

func cheapPass(ctx context.Context, names []string, roots resolve.Located, global string, o options) (res Result, err error) {
	if !supported || global == "" || len(names) == 0 {
		return Result{}, nil
	}
	var log *FileLog
	defer func() {
		if p := recover(); p != nil {
			if log != nil {
				log.Logf("engine", "panic", nil, fmt.Errorf("%v", p))
			}
			res, err = Result{}, nil
		}
	}()
	release, lerr := acquire(o.globalLockPath(global), 0)
	if lerr != nil {
		return Result{}, nil // a hook never waits on a repair
	}
	defer release()

	// Every located store is served; the targets only carry scope here, no
	// stamp is read or written.
	g := &target{needed: true}
	r, policy := repositoryTarget(roots)
	if r != nil {
		r.needed = true
	}
	log = newFileLog(global, roots.Store, roots.Checkout, o.ethos)
	env := Env{Roots: roots, Policy: policy, Global: global, Now: o.now(), Log: log}
	only := make(map[string]bool, len(names))
	for _, n := range names {
		only[n] = true
	}
	return runSteps(ctx, env, o, log, g, r, only), nil
}

// errBudget marks a step stopped by its own budget or Ensure's; the step is
// deferred, not failed, and the next entry resumes it.
var errBudget = errors.New("budget exceeded")

// errUnregistered marks a StepOrder name no unit has registered in this
// binary: its conversion has not run, so the stamps it would serve stay
// unwritten until a binary that carries it runs.
var errUnregistered = errors.New("step not registered in this binary")

// runSteps runs the steps in o.order, each against the targets its scope
// names. With only non-nil, just the named steps run and an unregistered name
// is skipped (a cheap pass is best effort); otherwise an unregistered name is
// a deferred step whose scope is unknown, so it blocks every needed stamp. A
// step that errors, defers, runs out of time or is abandoned blocks the stamps
// it serves and contributes nothing to the result; later steps still run.
func runSteps(ctx context.Context, env Env, o options, log *FileLog, g, r *target, only map[string]bool) (res Result) {
	ctx, cancel := context.WithTimeout(ctx, o.ensureBudget)
	defer cancel()
	for _, name := range o.order {
		if only != nil && !only[name] {
			continue
		}
		i := slices.IndexFunc(o.steps, func(s Step) bool { return s.Name == name })
		if i < 0 {
			if only != nil {
				continue
			}
			served := serves(ScopeBoth, g, r)
			if len(served) > 0 {
				log.Logf(name, "defer", nil, errUnregistered)
				block(served)
			}
			continue
		}
		s := o.steps[i]
		served := serves(s.Scope, g, r)
		if len(served) == 0 {
			continue
		}
		if ctx.Err() != nil {
			log.Logf(s.Name, "defer", nil, fmt.Errorf("ensure %w: %w", errBudget, ctx.Err()))
			block(served)
			continue
		}
		out, err := runStep(ctx, env, o.stepBudget, s)
		switch {
		case errors.Is(err, errBudget):
			log.Logf(s.Name, "defer", nil, err)
			block(served)
			continue
		case err != nil:
			log.Logf(s.Name, "error", nil, err)
			block(served)
			continue
		case out.Deferred:
			log.Logf(s.Name, "defer", nil, nil)
			block(served)
			continue
		}
		for _, t := range served {
			t.items += out.Items
		}
		served[0].residual = append(served[0].residual, out.Residuals...)
		res.Items += out.Items
		res.Residuals = append(res.Residuals, out.Residuals...)
		log.Logf(s.Name, "ran", nil, nil)
	}
	res.Repaired = res.Items > 0
	return res
}

// serves returns the needed targets a step of the given scope converts. The
// first is where its residuals are recorded.
func serves(sc Scope, g, r *target) []*target {
	var out []*target
	if (sc == ScopeGlobal || sc == ScopeBoth) && g.needed {
		out = append(out, g)
	}
	if (sc == ScopeRepository || sc == ScopeBoth) && r != nil && r.needed {
		out = append(out, r)
	}
	return out
}

func block(ts []*target) {
	for _, t := range ts {
		t.blocked = true
	}
}

type stepResult struct {
	out Outcome
	err error
}

// runStep runs s with the per-step budget in its own goroutine. A step that
// ignores its ctx is abandoned at the deadline: runStep returns errBudget,
// the goroutine finishes whenever it finishes, and its result goes nowhere. A
// panic in a step is an error: no step can fail the command that triggered
// the repair.
func runStep(ctx context.Context, env Env, budget time.Duration, s Step) (Outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	ch := make(chan stepResult, 1) // buffered: an abandoned step's send never blocks
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- stepResult{err: fmt.Errorf("step %s panicked: %v", s.Name, p)}
			}
		}()
		out, err := s.Run(ctx, env)
		ch <- stepResult{out: out, err: err}
	}()
	select {
	case r := <-ch:
		if r.err == nil && ctx.Err() != nil {
			// It returned, but after its time: its work is not known complete.
			return Outcome{}, fmt.Errorf("step %s: %s %w", s.Name, budget, errBudget)
		}
		return r.out, r.err
	case <-ctx.Done():
		return Outcome{}, fmt.Errorf("step %s: %s %w, abandoned", s.Name, budget, errBudget)
	}
}

// repositoryLock takes p.LockPath("autorepair", "repository"), inside the
// global lock.
func repositoryLock(p layout.Policy, wait time.Duration) (func(), error) {
	path, err := p.LockPath("autorepair", "repository")
	if err != nil {
		return nil, fmt.Errorf("naming the repository repair lock: %w", err)
	}
	if err := layout.EnsureLocalDir(p, filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("creating the repository repair lock directory: %w", err)
	}
	return acquire(path, wait)
}
