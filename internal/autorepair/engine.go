package autorepair

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// BinaryVersion is recorded in the stamp and the log. The commands set it
// before the first Ensure.
var BinaryVersion = "dev"

// Result is what one Ensure did. Items sums the conversions every step
// reported, deferred steps included. Residuals are the permanent conditions
// steps recorded; they never produce a line.
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

// Ensure brings the global store and, in a repository, the repository store to
// the current layout. Current stamps cost two stats and one small read.
// Otherwise it runs the registered steps in StepOrder under the repair locks.
// A repair problem is logged and left for the next entry; it never fails the
// caller, so the error is always nil.
func Ensure(ctx context.Context, roots resolve.Located, global string) (Result, error) {
	return ensure(ctx, roots, global, options{
		ethos:        BinaryVersion,
		now:          time.Now,
		stepBudget:   StepBudget,
		ensureBudget: EnsureBudget,
		lockWait:     LockWait,
		steps:        Steps,
		order:        StepOrder,
	})
}

// target is one store Ensure converts: its stamp, what is already recorded,
// and what this run learns.
type target struct {
	path     string
	ensure   func(dir string) error
	needed   bool
	blocked  bool // a step errored, deferred or ran out of time
	items    int
	residual []Residual
}

func (t *target) refresh() {
	s, err := ReadStamp(t.path)
	t.needed = err != nil || !s.Current()
}

func ensure(ctx context.Context, roots resolve.Located, global string, o options) (Result, error) {
	if !supported || global == "" {
		return Result{}, nil
	}
	g := &target{path: GlobalStampPath(global), ensure: func(d string) error { return mkdirPrivate(d) }}
	var r *target
	var policy layout.Policy
	if roots.Store != "" {
		if p, err := roots.Layout(); err == nil {
			policy = p
			r = &target{path: RepositoryStampPath(p), ensure: func(d string) error { return layout.EnsureLocalDir(p, d) }}
		}
	}
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

	release, err := acquire(o.globalLockPath(global), o.lockWait)
	if err != nil {
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

	log := NewFileLog(global, roots.Store, roots.Checkout, o.ethos)
	if r != nil && r.needed {
		if rel, err := repositoryLock(policy, o.lockWait); err != nil {
			log.Logf("engine", "defer-repository", []string{roots.Store}, err)
			r.needed = false
		} else {
			defer rel()
		}
	}
	if !g.needed && (r == nil || !r.needed) {
		return Result{}, nil
	}

	env := Env{Roots: roots, Policy: policy, Global: global, Now: o.now(), Log: log}
	res := runSteps(ctx, env, o, log, g, r)
	for _, t := range targets {
		if !t.needed {
			continue
		}
		if t.blocked {
			continue
		}
		s := Stamp{
			Version:    LayoutVersion,
			Ethos:      o.ethos,
			RepairedAt: env.Now.UTC().Format(time.RFC3339),
			Items:      t.items,
			Residual:   t.residual,
		}
		if err := WriteStamp(t.path, s, t.ensure); err != nil {
			log.Logf("engine", "write-stamp", []string{t.path}, err)
		}
	}
	return res, nil
}

// runSteps runs the steps in o.order, each against the targets its scope
// names. A step that errors, defers or runs out of time blocks the stamps it
// serves; later steps still run.
func runSteps(ctx context.Context, env Env, o options, log *FileLog, g, r *target) (res Result) {
	ctx, cancel := context.WithTimeout(ctx, o.ensureBudget)
	defer cancel()
	for _, name := range o.order {
		i := slices.IndexFunc(o.steps, func(s Step) bool { return s.Name == name })
		if i < 0 {
			continue // no unit has registered it yet
		}
		s := o.steps[i]
		served := serves(s.Scope, g, r)
		if len(served) == 0 {
			continue
		}
		if ctx.Err() != nil {
			log.Logf(s.Name, "defer", nil, errors.New("ensure budget spent"))
			block(served)
			continue
		}
		out, err := runStep(ctx, env, o.stepBudget, s)
		deferred := out.Deferred
		switch {
		case err != nil:
			log.Logf(s.Name, "error", nil, err)
			block(served)
		case ctx.Err() != nil:
			log.Logf(s.Name, "defer", nil, fmt.Errorf("step budget %s exceeded: %w", o.stepBudget, ctx.Err()))
			block(served)
		case deferred:
			log.Logf(s.Name, "defer", nil, nil)
			block(served)
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

// runStep runs s with the per-step budget. A panic is an error: no step can
// fail the command that triggered the repair.
func runStep(ctx context.Context, env Env, budget time.Duration, s Step) (out Outcome, err error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("step %s panicked: %v", s.Name, p)
		}
		if err == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("step %s: budget %s exceeded", s.Name, budget)
		}
	}()
	return s.Run(ctx, env)
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
