package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
//
// The global and repository repair locks belong to the Ensure until every
// goroutine it started has returned, an abandoned step's included: an
// abandoned step keeps the Env it was given, so the repository must stay
// locked while it runs. After the Ensure budget is spent the wait is unbounded
// by design. A step that never returns holds the locks; the next entry point
// defers at the lock wait and converts nothing, which is the correct
// degradation. The first abandonment also ends the pass: no later step runs,
// since the abandoned one may hold inner locks the next would contend for.
func Ensure(ctx context.Context, roots resolve.Located, global string) (Result, error) {
	return ensure(ctx, roots, global, defaultOptions())
}

// CheapPass runs the named steps now, whatever the stamps say, and never
// writes a stamp. It is the only supported entry for the per-event
// conversions DES-084 names; a hook does not build its own pass, Env, log or
// summary line. Session start calls CheapPass(ctx, []string{"sessions"}, env)
// and appends Result.Line() to the session-start note: the design has exactly
// one "upgraded storage" line. Pre-commit calls it with "live-tails" and
// "hook-sections" (untrack-local is not a commit-time step: it stages
// record-tree deletions, which must not ride along in the user's commit).
// The env comes from HookEnv.
//
// It takes the global repair lock without waiting and skips the pass when the
// lock is busy, takes the repository repair lock the same way (a busy
// repository lock hides the repository from the steps), builds the Env the way
// Ensure does from env.Roots and env.Global, runs only the named steps that
// are registered, in StepOrder, logs under the lock, and returns Ensure's
// Result shape. A name no unit registers is skipped, not an error.
func CheapPass(ctx context.Context, names []string, env Env) (Result, error) {
	return cheapPass(ctx, names, env.Roots, env.Global, defaultOptions())
}

// HookEnv is the Env a hook passes to CheapPass: the same one Ensure builds
// before it decides what to show a step, with the located roots and policy
// (zero unless a repository that has used ethos was located cleanly), the
// global root, the clock and the repair log. It writes nothing. A step sees
// the repository only inside CheapPass, under the repository repair lock.
func HookEnv(roots resolve.Located, global string) (Env, error) {
	return hookEnv(roots, global, defaultOptions())
}

func hookEnv(roots resolve.Located, global string, o options) (Env, error) {
	if global == "" {
		return Env{}, errors.New("building the hook environment: no global store root")
	}
	r, policy := repositoryTarget(roots)
	var store, checkout string
	if roots.Err() == nil {
		store, checkout = roots.Store, roots.Checkout
	}
	env := Env{Global: global, Now: o.now(), Log: newFileLog(global, store, checkout, o.ethos)}
	if r != nil {
		env.Roots, env.Policy = roots, policy
	}
	return env, nil
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
	if !supported || global == "" || ctx.Err() != nil {
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
	missing := unregistered(o)
	if len(missing) > 0 && !anyRegisteredServes(o, g, r) {
		// Nothing this binary carries would run, and the stamps stay unwritten
		// until a binary that carries every step runs: there is nothing to
		// lock or log. The stores are left byte-identical.
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

	log = openLog(global, roots, o.ethos)
	if r != nil && r.needed {
		// A step is shown the repository only while this Ensure holds its
		// lock; a busy lock drops the repository pass.
		if rel, rerr := repositoryLock(policy, o.lockWait); rerr != nil {
			log.Logf("engine", "defer-repository", nil, rerr)
			r.needed = false
		} else {
			defer rel()
		}
	}
	if !g.needed && (r == nil || !r.needed) {
		return Result{}, nil
	}

	if len(missing) > 0 {
		// Unknown scope, so every needed stamp is withheld; one line names
		// the set so the log does not grow by one line per name per command.
		log.Logf("engine", "defer", nil, fmt.Errorf("%w: %s", errUnregistered, strings.Join(missing, ", ")))
		block(serves(ScopeBoth, g, r))
	}
	env := Env{Global: global, Now: o.now(), Log: log}
	var live liveSteps
	defer live.wait(log) // registered last, so it runs before either lock is released
	res = runSteps(ctx, env, roots, policy, o, log, g, r, nil, &live)
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
	if !supported || global == "" || len(names) == 0 || ctx.Err() != nil {
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
	// Every located store is served; the targets only carry scope here, no
	// stamp is read or written.
	g := &target{needed: true}
	r, policy := repositoryTarget(roots)
	if r != nil {
		r.needed = true
	}
	if !namedStepServes(names, o, g, r) {
		// Nothing named would run: take no lock and leave no file, as Ensure
		// does when no registered step serves a needed target.
		return Result{}, nil
	}
	release, lerr := acquire(o.globalLockPath(global), 0)
	if lerr != nil {
		return Result{}, nil // a hook never waits on a repair
	}
	defer release()

	log = openLog(global, roots, o.ethos)
	if r != nil {
		r.needed = true
		if rel, rerr := repositoryLock(policy, 0); rerr != nil {
			log.Logf("engine", "defer-repository", nil, rerr)
			r.needed = false
		} else {
			defer rel()
		}
	}
	env := Env{Global: global, Now: o.now(), Log: log}
	only := make(map[string]bool, len(names))
	for _, n := range names {
		only[n] = true
	}
	var live liveSteps
	defer live.wait(log) // registered last, so it runs before either lock is released
	return runSteps(ctx, env, roots, policy, o, log, g, r, only, &live), nil
}

// openLog opens the repair log tagged with the roots. A repository whose
// location carries an error is not served (repositoryTarget returns nil for
// it); the failure is written to the log so the skip is visible, not read as
// "no repository".
func openLog(global string, roots resolve.Located, ethos string) *FileLog {
	log := newFileLog(global, roots.Store, roots.Checkout, ethos)
	if err := roots.Err(); err != nil {
		log.Logf("engine", "skip-repository", nil, err)
	}
	return log
}

// errBudget marks a step stopped by its own budget or Ensure's; the step is
// deferred, not failed, and the next entry resumes it.
var errBudget = errors.New("budget exceeded")

// errUnregistered marks StepOrder names no unit has registered in this
// binary: their conversions have not run, so the stamps stay unwritten until
// a binary that carries them runs.
var errUnregistered = errors.New("steps not registered in this binary")

// unregistered is the StepOrder names with no registered step, in order.
func unregistered(o options) []string {
	var out []string
	for _, name := range o.order {
		if !slices.ContainsFunc(o.steps, func(s Step) bool { return s.Name == name }) {
			out = append(out, name)
		}
	}
	return out
}

// namedStepServes reports whether a registered step in o.order, one of names,
// would run against a needed target.
func namedStepServes(names []string, o options, g, r *target) bool {
	for _, name := range o.order {
		if !slices.Contains(names, name) {
			continue
		}
		i := slices.IndexFunc(o.steps, func(s Step) bool { return s.Name == name })
		if i >= 0 && len(serves(o.steps[i].Scope, g, r)) > 0 {
			return true
		}
	}
	return false
}

// anyRegisteredServes reports whether a registered step in o.order would run
// against a needed target.
func anyRegisteredServes(o options, g, r *target) bool {
	for _, name := range o.order {
		i := slices.IndexFunc(o.steps, func(s Step) bool { return s.Name == name })
		if i >= 0 && len(serves(o.steps[i].Scope, g, r)) > 0 {
			return true
		}
	}
	return false
}

// runSteps runs the registered steps in o.order, each against the targets
// its scope names. With only non-nil, just the named steps run (a cheap
// pass). A name with no registered step is skipped here; ensure has already
// withheld the stamps for it. A step that errors, defers, runs out of time or
// is abandoned blocks the stamps it serves and contributes nothing to the
// result; later steps still run, except after an abandoned step: the pass
// stops there.
func runSteps(ctx context.Context, base Env, roots resolve.Located, policy layout.Policy, o options, log *FileLog, g, r *target, only map[string]bool, live *liveSteps) (res Result) {
	ctx, cancel := context.WithTimeout(ctx, o.ensureBudget)
	defer cancel()
	for _, name := range o.order {
		if only != nil && !only[name] {
			continue
		}
		i := slices.IndexFunc(o.steps, func(s Step) bool { return s.Name == name })
		if i < 0 {
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
		env := base
		if r != nil && r.needed && s.Scope != ScopeGlobal {
			// r.needed is true only while this pass holds the repository lock.
			env.Roots, env.Policy = roots, policy
		}
		out, err := runStep(ctx, env, o.stepBudget, s, stepLockPath(base.Global, s.Name), live)
		switch {
		case errors.Is(err, errAbandoned):
			log.Logf(s.Name, "defer", nil, err)
			log.Logf(s.Name, "stop", nil, fmt.Errorf("pass stopped after abandoned step %s", s.Name))
			// The steps after this one are skipped, so every target the pass
			// could serve stays unstamped, not just this step's.
			block(serves(ScopeBoth, g, r))
			res.Repaired = res.Items > 0
			return res
		case errors.Is(err, errInFlight):
			log.Logf(s.Name, "defer", nil, err)
			block(served)
			continue
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
		for _, rs := range out.Residuals {
			if t := residualTarget(rs, served, r); t != nil {
				t.residual = append(t.residual, rs)
			}
		}
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

// errAbandoned marks a step still running after its budget; it is an errBudget.
var errAbandoned = fmt.Errorf("abandoned: %w", errBudget)

// liveSteps tracks the goroutines runStep started, so the Ensure that owns the
// repair locks can hold them until every one has returned.
type liveSteps struct {
	wg        sync.WaitGroup
	abandoned string
}

// wait blocks until every started step has returned, logging once when an
// abandoned one makes it wait.
func (l *liveSteps) wait(log *FileLog) {
	if l.abandoned != "" {
		log.Logf(l.abandoned, "wait", nil, fmt.Errorf("waiting for abandoned step %s", l.abandoned))
	}
	l.wg.Wait()
}

type stepResult struct {
	out Outcome
	err error
}

// errInFlight marks a step whose previous run, abandoned at its budget, has
// not returned: starting it again would run two instances at once.
var errInFlight = errors.New("previous run still in flight")

// stepLockPath is the lock a step's run holds from start until its goroutine
// returns, even when Ensure has abandoned it. It sits beside the global repair
// lock, so another process sees an abandoned step as a busy lock too, and the
// kernel frees it if the process dies.
func stepLockPath(global, name string) string {
	return filepath.Join(global, "locks", "step-"+name+".lock")
}

// runStep runs s with the per-step budget in its own goroutine. A step that
// ignores its ctx is abandoned at the deadline: runStep returns errBudget,
// the goroutine finishes whenever it finishes, and its result goes nowhere.
// The step's lock is held until the goroutine returns, so runStep returns
// errInFlight, without starting s, while an earlier run is still going. A
// panic in a step is an error: no step can fail the command that triggered
// the repair.
func runStep(ctx context.Context, env Env, budget time.Duration, s Step, lockPath string, live *liveSteps) (Outcome, error) {
	release, err := acquire(lockPath, 0)
	if errors.Is(err, errBusy) {
		return Outcome{}, fmt.Errorf("step %s: %w", s.Name, errInFlight)
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("step %s: %w", s.Name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	ch := make(chan stepResult, 1) // buffered: an abandoned step's send never blocks
	live.wg.Add(1)
	go func() {
		defer live.wg.Done() // after release: the step lock goes first
		defer release()
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
		live.abandoned = s.Name
		return Outcome{}, fmt.Errorf("step %s: %s %w", s.Name, budget, errAbandoned)
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

// residualTarget is the stamp a residual belongs to, or nil when this pass
// serves no stamp of its scope. A residual that names its Scope goes only to
// that store's stamp. One that does not goes to the repository's when the step
// ran for it, else to the first target served; a step that runs for one store
// has only that store to name.
func residualTarget(rs Residual, served []*target, r *target) *target {
	if rs.Scope == ScopeRepository || rs.Scope == ScopeGlobal {
		for _, t := range served {
			if (rs.Scope == ScopeRepository) == (t == r) {
				return t
			}
		}
		return nil
	}
	for _, t := range served {
		if t == r {
			return t
		}
	}
	return served[0]
}
