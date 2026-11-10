package autorepair

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// rig is a global store and, optionally, a repository, with fake steps.
type rig struct {
	t      *testing.T
	global string
	store  string
	roots  resolve.Located
	opts   options
	ran    []string
	mu     sync.Mutex
}

// newRig builds a rig. inRepo adds a repository that has used ethos (it has a
// .punt-labs directory); newPlainRepoRig adds one that never has.
func newRig(t *testing.T, inRepo bool) *rig {
	t.Helper()
	r := &rig{t: t, global: filepath.Join(physicalTemp(t), "global")}
	if inRepo {
		r.store = physicalTemp(t)
		r.roots = resolve.Located{Store: r.store, Checkout: r.store}
		require.NoError(t, os.MkdirAll(filepath.Join(r.store, ".punt-labs"), 0o755))
	}
	r.opts = options{
		ethos:        "v5.0.0",
		now:          func() time.Time { return time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC) },
		stepBudget:   2 * time.Second,
		ensureBudget: 10 * time.Second,
		lockWait:     2 * time.Second,
	}
	return r
}

// step registers a fake step that records its name and returns out, err.
func (r *rig) step(name string, scope Scope, out Outcome, err error) {
	r.opts.steps = append(r.opts.steps, Step{Name: name, Scope: scope,
		Run: func(context.Context, Env) (Outcome, error) {
			r.mu.Lock()
			r.ran = append(r.ran, name)
			r.mu.Unlock()
			return out, err
		}})
	r.opts.order = append(r.opts.order, name)
}

func (r *rig) ensure() Result {
	r.t.Helper()
	res, err := ensure(context.Background(), r.roots, r.global, r.opts)
	require.NoError(r.t, err)
	return res
}

func (r *rig) globalStamp() (Stamp, error) { return ReadStamp(GlobalStampPath(r.global)) }

func (r *rig) repoStamp() (Stamp, error) {
	return ReadStamp(filepath.Join(r.store, ".punt-labs", "local", "ethos", StampName))
}

func TestEnsureRunsStepsInStepOrder(t *testing.T) {
	r := newRig(t, false)
	// Registration order differs from StepOrder.
	r.opts.order = []string{"a", "b", "c"}
	for _, n := range []string{"c", "a", "b"} {
		r.step(n, ScopeGlobal, Outcome{}, nil)
	}
	r.opts.order = []string{"a", "b", "c"}
	r.ensure()
	assert.Equal(t, []string{"a", "b", "c"}, r.ran)
}

func TestEnsureWritesStampsAndIsIdempotent(t *testing.T) {
	r := newRig(t, true)
	r.step("one", ScopeBoth, Outcome{Items: 3}, nil)
	res := r.ensure()
	assert.Equal(t, 3, res.Items)
	assert.True(t, res.Repaired)

	g, err := r.globalStamp()
	require.NoError(t, err)
	assert.Equal(t, LayoutVersion, g.Version)
	assert.Equal(t, "v5.0.0", g.Ethos)
	assert.Equal(t, "2026-11-09T10:00:00Z", g.RepairedAt)
	assert.Equal(t, 3, g.Items)
	rs, err := r.repoStamp()
	require.NoError(t, err)
	assert.True(t, rs.Current())

	repoStampPath := filepath.Join(r.store, ".punt-labs", "local", "ethos", StampName)
	gi, err := os.Stat(GlobalStampPath(r.global))
	require.NoError(t, err)
	ri, err := os.Stat(repoStampPath)
	require.NoError(t, err)
	gBytes, err := os.ReadFile(GlobalStampPath(r.global))
	require.NoError(t, err)
	rBytes, err := os.ReadFile(repoStampPath)
	require.NoError(t, err)
	logBefore, err := os.ReadFile(LogPath(r.global))
	require.NoError(t, err)

	res = r.ensure()
	assert.Equal(t, Result{}, res)
	assert.Equal(t, []string{"one"}, r.ran, "a current stamp runs no step")
	gi2, err := os.Stat(GlobalStampPath(r.global))
	require.NoError(t, err)
	assert.Equal(t, gi.ModTime(), gi2.ModTime(), "global stamp untouched")
	ri2, err := os.Stat(repoStampPath)
	require.NoError(t, err)
	assert.Equal(t, ri.ModTime(), ri2.ModTime(), "repository stamp untouched")
	gBytes2, err := os.ReadFile(GlobalStampPath(r.global))
	require.NoError(t, err)
	assert.Equal(t, string(gBytes), string(gBytes2), "global stamp bytes unchanged")
	rBytes2, err := os.ReadFile(repoStampPath)
	require.NoError(t, err)
	assert.Equal(t, string(rBytes), string(rBytes2), "repository stamp bytes unchanged")
	logAfter, err := os.ReadFile(LogPath(r.global))
	require.NoError(t, err)
	assert.Equal(t, string(logBefore), string(logAfter), "no log line on the fast path")
}

// With no step registered nothing has been converted, so nothing may be
// stamped current: a stamp written now would make Ensure take the fast path
// forever once the real steps land. Nothing has run either, so there is
// nothing to log or lock: two runs leave both stores byte-identical and print
// nothing. (A test that hashes a fake HOME around a read-only command relies
// on this.)
func TestEnsureZeroStepsWritesNoStamp(t *testing.T) {
	r := newRig(t, true)
	r.opts.order = []string{"a", "b"}
	require.NoError(t, os.MkdirAll(r.global, 0o700))
	before := treeOf(t, r.global)
	repoBefore := treeOf(t, r.store)
	for range 2 {
		res := r.ensure()
		assert.Equal(t, "", res.Line())
		assert.False(t, res.Repaired)
		_, err := r.globalStamp()
		assert.ErrorIs(t, err, os.ErrNotExist, "global stamp")
		_, err = r.repoStamp()
		assert.ErrorIs(t, err, os.ErrNotExist, "repository stamp")
	}
	assert.Equal(t, before, treeOf(t, r.global), "the global store is untouched: no log, no lock")
	assert.Equal(t, repoBefore, treeOf(t, r.store), "the repository is untouched")
}

// treeOf lists every path under root, relative, for before/after checks.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	}))
	return out
}

// A StepOrder name with no registration is a step that has not run: it
// blocks the stamps it would serve. The engine cannot know its scope, so it
// blocks both. Registered steps still run, and the missing set is logged
// once per Ensure, not once per name.
func TestEnsureUnregisteredOrderNameBlocksStamps(t *testing.T) {
	r := newRig(t, true)
	r.step("present", ScopeBoth, Outcome{Items: 1}, nil)
	r.opts.order = append(r.opts.order, "absent", "also-absent")
	res := r.ensure()
	assert.Equal(t, []string{"present"}, r.ran)
	assert.Equal(t, 1, res.Items)
	_, err := r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = r.repoStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)
	var deferred []map[string]any
	for _, l := range readLog(t, LogPath(r.global)) {
		if l["action"] == "defer" {
			deferred = append(deferred, l)
		}
	}
	require.Len(t, deferred, 1, "one line for the missing set")
	assert.Equal(t, "engine", deferred[0]["step"])
	assert.Contains(t, deferred[0]["error"], "absent, also-absent")
	assert.Contains(t, deferred[0]["error"], "not registered")
}

// A git repository that never used ethos has no .punt-labs directory. Ensure
// leaves it byte-identical: no stamp, no lock, no local zone, so `git add -A`
// there has nothing of ours to commit.
func TestEnsurePlainRepositoryIsLeftUntouched(t *testing.T) {
	r := newRig(t, false)
	r.store = physicalTemp(t)
	r.roots = resolve.Located{Store: r.store, Checkout: r.store}
	require.NoError(t, os.WriteFile(filepath.Join(r.store, "README"), []byte("x"), 0o644))
	r.step("both", ScopeBoth, Outcome{Items: 1}, nil)
	r.step("repo", ScopeRepository, Outcome{Items: 1}, nil)

	r.ensure()
	entries, err := os.ReadDir(r.store)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"README"}, names, "nothing was created in the repository")
	assert.Equal(t, []string{"both"}, r.ran, "the repository scope was not served")
	g, err := r.globalStamp()
	require.NoError(t, err)
	assert.True(t, g.Current(), "the global pass still runs")
}

// In a plain repository the ScopeBoth step that does run must not be told it
// is in a repository, or it would touch what the engine chose to leave alone.
func TestEnsurePlainRepositoryIsNotShownToSteps(t *testing.T) {
	r := newRig(t, false)
	r.store = physicalTemp(t)
	r.roots = resolve.Located{Store: r.store, Checkout: r.store}
	var got Env
	r.opts.steps = []Step{{Name: "both", Scope: ScopeBoth, Run: func(_ context.Context, e Env) (Outcome, error) {
		got = e
		return Outcome{}, nil
	}}}
	r.opts.order = []string{"both"}
	r.ensure()
	assert.False(t, got.InRepository())
	assert.Equal(t, "", got.Roots.Store, "the unserved repository's roots are withheld")
	assert.Equal(t, "", got.Policy.LocalRoot())
}

// A repository whose location carries an error is not served either, and the
// skip is recorded in the log rather than silently treated as "no repository".
func TestEnsureLocateFailureSkipsRepositoryAndLogsIt(t *testing.T) {
	r := newRig(t, true)
	r.roots.StoreErr = errors.New("dangling store symlink")
	r.step("repo", ScopeRepository, Outcome{Items: 1}, nil)
	r.step("glob", ScopeGlobal, Outcome{Items: 1}, nil)
	r.ensure()
	assert.Equal(t, []string{"glob"}, r.ran)
	_, err := r.repoStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)
	raw, err := os.ReadFile(LogPath(r.global))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"action":"skip-repository"`)
	assert.Contains(t, string(raw), "dangling store symlink")
}

// A step that ignores its deadline is abandoned at the budget: the stamp is
// blocked, the pass stops (no later step runs), the step's late result is
// discarded, and Ensure returns only once the step has, because it owns the
// locks until then.
func TestEnsureStepIgnoringDeadlineIsAbandoned(t *testing.T) {
	r := newRig(t, false)
	r.opts.stepBudget = 50 * time.Millisecond
	release := make(chan struct{})
	finished := make(chan struct{})
	r.opts.steps = []Step{{Name: "hang", Scope: ScopeGlobal,
		Run: func(context.Context, Env) (Outcome, error) {
			<-release // ignores ctx entirely
			close(finished)
			return Outcome{Items: 7}, nil
		}}}
	r.opts.order = []string{"hang"}
	r.step("after", ScopeGlobal, Outcome{}, nil)

	done := make(chan Result, 1)
	go func() { done <- r.ensure() }()
	select {
	case <-done:
		t.Fatal("Ensure returned while the abandoned step still ran")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case res := <-done:
		assert.Equal(t, 0, res.Items, "the abandoned step's items are discarded")
		assert.False(t, res.Repaired)
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not return after the abandoned step did")
	}
	assert.Empty(t, r.ran, "no step runs after an abandoned one")
	_, err := r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)
	raw, err := os.ReadFile(LogPath(r.global))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"step":"hang"`)
	assert.Contains(t, string(raw), "budget")
	assert.Contains(t, string(raw), "waiting for abandoned step hang")
	assert.Contains(t, string(raw), "pass stopped after abandoned step hang")
	<-finished
}

// Items from a step that errored or deferred are not conversions: they do not
// set Repaired, do not count, and the step is not logged as "ran".
func TestEnsureCountsOnlyFinishedSteps(t *testing.T) {
	r := newRig(t, false)
	r.step("errored", ScopeGlobal, Outcome{Items: 3}, errors.New("boom"))
	r.step("deferred", ScopeGlobal, Outcome{Items: 2, Deferred: true}, nil)
	r.step("ok", ScopeGlobal, Outcome{Items: 1}, nil)
	res := r.ensure()
	assert.Equal(t, 1, res.Items)
	assert.True(t, res.Repaired)

	lines := readLog(t, LogPath(r.global))
	ran := map[string]bool{}
	for _, l := range lines {
		if l["action"] == "ran" {
			ran[l["step"].(string)] = true
		}
	}
	assert.Equal(t, map[string]bool{"ok": true}, ran, `"ran" is logged only on success`)

	r2 := newRig(t, false)
	r2.step("deferred", ScopeGlobal, Outcome{Items: 2, Deferred: true}, nil)
	res = r2.ensure()
	assert.Equal(t, Result{}, res, "a deferred step alone repairs nothing")
}

// A panic outside any step (in the engine itself) never fails the caller.
func TestEnsureRecoversFromEnginePanic(t *testing.T) {
	r := newRig(t, false)
	r.step("s", ScopeGlobal, Outcome{Items: 1}, nil)
	r.opts.now = func() time.Time { panic("clock") }
	var res Result
	var err error
	assert.NotPanics(t, func() { res, err = ensure(context.Background(), r.roots, r.global, r.opts) })
	assert.NoError(t, err)
	assert.Equal(t, Result{}, res)
	_, serr := r.globalStamp()
	assert.ErrorIs(t, serr, os.ErrNotExist)
}

func TestEnsureDeferredStepLeavesNoStamp(t *testing.T) {
	r := newRig(t, false)
	r.step("busy", ScopeGlobal, Outcome{Deferred: true, Items: 1}, nil)
	res := r.ensure()
	assert.Equal(t, 0, res.Items, "a deferred step's count is not a conversion")
	_, err := r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)

	// The next entry resumes: the step runs again.
	r.ensure()
	assert.Equal(t, []string{"busy", "busy"}, r.ran)
}

func TestEnsureStepErrorAndPanicLeaveNoStamp(t *testing.T) {
	for name, run := range map[string]func(context.Context, Env) (Outcome, error){
		"error": func(context.Context, Env) (Outcome, error) { return Outcome{}, errors.New("boom") },
		"panic": func(context.Context, Env) (Outcome, error) { panic("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, false)
			r.opts.steps = []Step{{Name: "bad", Scope: ScopeGlobal, Run: run}}
			r.opts.order = []string{"bad"}
			r.step("after", ScopeGlobal, Outcome{}, nil)
			res := r.ensure() // must not fail the caller
			assert.Equal(t, "", res.Line())
			assert.Equal(t, []string{"after"}, r.ran, "later steps still run")
			_, err := r.globalStamp()
			assert.ErrorIs(t, err, os.ErrNotExist)
			raw, err := os.ReadFile(LogPath(r.global))
			require.NoError(t, err)
			assert.Contains(t, string(raw), `"step":"bad"`)
			assert.Contains(t, string(raw), "boom")
		})
	}
}

func TestEnsureStepPastBudgetIsLoggedAndLeavesNoStamp(t *testing.T) {
	r := newRig(t, false)
	r.opts.stepBudget = 30 * time.Millisecond
	r.opts.steps = []Step{{Name: "slow", Scope: ScopeGlobal,
		Run: func(ctx context.Context, _ Env) (Outcome, error) {
			<-ctx.Done()
			return Outcome{Items: 2}, nil // ignores that it ran out of time
		}}}
	r.opts.order = []string{"slow"}
	res := r.ensure()
	assert.Equal(t, 0, res.Items, "work past the budget is not known complete")
	_, err := r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)
	raw, err := os.ReadFile(LogPath(r.global))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "budget")
}

func TestEnsureTotalBudgetStopsRemainingSteps(t *testing.T) {
	r := newRig(t, false)
	r.opts.ensureBudget = 30 * time.Millisecond
	r.opts.steps = []Step{{Name: "slow", Scope: ScopeGlobal,
		Run: func(ctx context.Context, _ Env) (Outcome, error) {
			<-ctx.Done()
			return Outcome{}, nil
		}}}
	r.opts.order = []string{"slow"}
	r.step("later", ScopeGlobal, Outcome{}, nil)
	r.ensure()
	assert.Empty(t, r.ran, "no step starts after the total budget")
	_, err := r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestEnsureConcurrentCallsConvertOnce(t *testing.T) {
	r := newRig(t, true)
	var runs atomic.Int32
	r.opts.steps = []Step{{Name: "once", Scope: ScopeBoth,
		Run: func(context.Context, Env) (Outcome, error) {
			runs.Add(1)
			time.Sleep(150 * time.Millisecond)
			return Outcome{Items: 1}, nil
		}}}
	r.opts.order = []string{"once"}

	var wg sync.WaitGroup
	results := make([]Result, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := ensure(context.Background(), r.roots, r.global, r.opts)
			assert.NoError(t, err)
			results[i] = res
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, runs.Load(), "one conversion")
	repaired := 0
	for _, res := range results {
		if res.Repaired {
			repaired++
		}
	}
	assert.Equal(t, 1, repaired)
	g, err := r.globalStamp()
	require.NoError(t, err)
	assert.True(t, g.Current())
	rs, err := r.repoStamp()
	require.NoError(t, err)
	assert.True(t, rs.Current())
}

func TestEnsureBusyLockContinuesWithoutFailing(t *testing.T) {
	r := newRig(t, false)
	r.opts.lockWait = 50 * time.Millisecond
	r.step("s", ScopeGlobal, Outcome{Items: 1}, nil)

	release, err := acquire(r.opts.globalLockPath(r.global), time.Second)
	require.NoError(t, err)
	defer release()

	res := r.ensure()
	assert.Equal(t, Result{}, res)
	assert.Empty(t, r.ran)
	_, err = r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestEnsureHigherVersionIsCurrent(t *testing.T) {
	r := newRig(t, true)
	r.step("s", ScopeBoth, Outcome{Items: 1}, nil)
	require.NoError(t, WriteStamp(GlobalStampPath(r.global), Stamp{Version: 6}, func(d string) error { return os.MkdirAll(d, 0o700) }))
	repoDir := filepath.Join(r.store, ".punt-labs", "local", "ethos")
	require.NoError(t, WriteStamp(filepath.Join(repoDir, StampName), Stamp{Version: 6}, func(d string) error { return os.MkdirAll(d, 0o700) }))

	res := r.ensure()
	assert.Equal(t, Result{}, res)
	assert.Empty(t, r.ran, "a stamp from a newer binary is never converted backward")
	g, err := r.globalStamp()
	require.NoError(t, err)
	assert.Equal(t, 6, g.Version, "the newer stamp is not overwritten")
}

func TestEnsureCorruptStampIsRepaired(t *testing.T) {
	r := newRig(t, false)
	r.step("s", ScopeGlobal, Outcome{Items: 1}, nil)
	require.NoError(t, os.MkdirAll(r.global, 0o700))
	require.NoError(t, os.WriteFile(GlobalStampPath(r.global), []byte("version: ["), 0o600))
	r.ensure()
	assert.Equal(t, []string{"s"}, r.ran)
	g, err := r.globalStamp()
	require.NoError(t, err)
	assert.True(t, g.Current())
}

func TestEnsureRepositoryScopeSkippedOutsideRepository(t *testing.T) {
	r := newRig(t, false)
	r.step("repo", ScopeRepository, Outcome{Items: 5}, nil)
	r.step("glob", ScopeGlobal, Outcome{Items: 1}, nil)
	res := r.ensure()
	assert.Equal(t, []string{"glob"}, r.ran)
	assert.Equal(t, 1, res.Items)
}

func TestEnsureScopesStampsIndependently(t *testing.T) {
	r := newRig(t, true)
	r.step("g", ScopeGlobal, Outcome{}, nil)
	r.step("r", ScopeRepository, Outcome{Deferred: true}, nil)
	r.ensure()
	g, err := r.globalStamp()
	require.NoError(t, err)
	assert.True(t, g.Current(), "the global stamp does not wait for a repository step")
	_, err = r.repoStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)

	r.ran = nil
	r.ensure()
	assert.Equal(t, []string{"r"}, r.ran, "the current global stamp skips global steps")
}

func TestEnsureRecordsResidualsInStamp(t *testing.T) {
	r := newRig(t, true)
	res1 := Residual{Step: "round-decisions", ID: "m-1", Reason: "incomplete history"}
	res2 := Residual{Step: "global-audit", ID: "s-1", Reason: "unreadable"}
	r.step("round-decisions", ScopeRepository, Outcome{Residuals: []Residual{res1}}, nil)
	r.step("global-audit", ScopeGlobal, Outcome{Residuals: []Residual{res2}}, nil)
	res := r.ensure()
	assert.Equal(t, "", res.Line(), "a residual never produces a line")
	assert.Equal(t, []Residual{res1, res2}, res.Residuals)
	rs, err := r.repoStamp()
	require.NoError(t, err)
	assert.Equal(t, []Residual{res1}, rs.Residual)
	g, err := r.globalStamp()
	require.NoError(t, err)
	assert.Equal(t, []Residual{res2}, g.Residual)

	r.ensure()
	assert.Equal(t, []string{"round-decisions", "global-audit"}, r.ran, "a recorded residual is not retried")
}

func TestEnsureStepSeesEnvironment(t *testing.T) {
	r := newRig(t, true)
	var got Env
	r.opts.steps = []Step{{Name: "env", Scope: ScopeBoth, Run: func(_ context.Context, e Env) (Outcome, error) {
		got = e
		e.Log.Logf("env", "probe", []string{"/x"}, nil)
		return Outcome{}, nil
	}}}
	r.opts.order = []string{"env"}
	r.ensure()
	assert.True(t, got.InRepository())
	assert.Equal(t, r.store, got.Roots.Store)
	assert.Equal(t, r.global, got.Global)
	assert.Equal(t, r.store, filepath.Dir(filepath.Dir(filepath.Dir(got.Policy.LocalRoot()))))
	assert.False(t, got.Now.IsZero())
	raw, err := os.ReadFile(LogPath(r.global))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"action":"probe"`)
}

func TestResultLine(t *testing.T) {
	assert.Equal(t, "", Result{}.Line())
	assert.Equal(t, "", Result{Items: 0}.Line())
	assert.Equal(t, "ethos: upgraded storage (3 items; see ~/.punt-labs/ethos/logs/repair.log)",
		Result{Items: 3, Repaired: true}.Line())
	assert.Equal(t, "ethos: upgraded storage (1 items; see ~/.punt-labs/ethos/logs/repair.log)",
		Result{Items: 1, Repaired: true}.Line())
}

func TestEnsureNoGlobalRootIsNoop(t *testing.T) {
	res, err := Ensure(context.Background(), resolve.Located{}, "")
	require.NoError(t, err)
	assert.Equal(t, Result{}, res)
}

// TestRegisteredStepsAreInStepOrder keeps the real registry and StepOrder
// consistent. A name in StepOrder with no registration is logged, not failed,
// so step units can land independently; a registered step that is not in
// StepOrder, or one registered twice, fails.
func TestRegisteredStepsAreInStepOrder(t *testing.T) {
	order := map[string]bool{}
	for _, n := range StepOrder {
		assert.False(t, order[n], "StepOrder names %q twice", n)
		order[n] = true
	}
	registered := map[string]int{}
	for _, s := range Steps {
		registered[s.Name]++
		assert.True(t, order[s.Name], "step %q is registered but not in StepOrder", s.Name)
		assert.NotNil(t, s.Run, "step %q has no Run", s.Name)
		assert.Contains(t, []Scope{ScopeGlobal, ScopeRepository, ScopeBoth}, s.Scope, "step %q has no scope", s.Name)
	}
	for n, c := range registered {
		assert.Equal(t, 1, c, "step %q is registered %d times", n, c)
	}
	for _, n := range StepOrder {
		if registered[n] == 0 {
			t.Logf("StepOrder names %q, which no unit has registered yet", n)
		}
	}
}
