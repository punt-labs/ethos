package autorepair

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bothProbe registers a ScopeBoth step that records the Env it was shown.
func (r *rig) bothProbe(got *Env) {
	r.opts.steps = []Step{{Name: "both", Scope: ScopeBoth, Run: func(_ context.Context, e Env) (Outcome, error) {
		*got = e
		return Outcome{}, nil
	}}}
	r.opts.order = []string{"both"}
}

// A step sees the repository only while this Ensure holds the repository repair
// lock. When another holder has it, the repository pass is dropped and the step
// is shown no roots and no policy.
func TestEnsureRepositoryLockBusyHidesRepository(t *testing.T) {
	r := newRig(t, true)
	r.opts.lockWait = 50 * time.Millisecond
	_, p := repositoryTarget(r.roots)
	rel, err := repositoryLock(p, time.Second)
	require.NoError(t, err)
	defer rel()
	var got Env
	r.bothProbe(&got)
	r.ensure()
	assert.False(t, got.InRepository())
	assert.Equal(t, "", got.Roots.Store)
	assert.Equal(t, "", got.Policy.LocalRoot())
}

// A current repository stamp with a missing global stamp: the repository pass
// is not active and takes no lock, so the step is shown no repository.
func TestEnsureHidesRepositoryWhenOnlyGlobalNeeded(t *testing.T) {
	r := newRig(t, true)
	r.step("both", ScopeBoth, Outcome{}, nil)
	r.ensure() // both stamps current
	require.NoError(t, os.Remove(GlobalStampPath(r.global)))
	var got Env
	r.bothProbe(&got)
	r.ensure()
	assert.False(t, got.InRepository())
	assert.Equal(t, "", got.Roots.Store)
	assert.Equal(t, "", got.Policy.LocalRoot())
}

// While a step runs for the repository, the repository repair lock is held.
func TestEnsureRepositoryStepRunsUnderRepositoryLock(t *testing.T) {
	r := newRig(t, true)
	_, p := repositoryTarget(r.roots)
	var busy bool
	var got Env
	r.opts.steps = []Step{{Name: "both", Scope: ScopeBoth, Run: func(_ context.Context, e Env) (Outcome, error) {
		got = e
		rel, err := repositoryLock(p, 0)
		if err == nil {
			rel()
		}
		busy = err != nil
		return Outcome{}, nil
	}}}
	r.opts.order = []string{"both"}
	r.ensure()
	assert.True(t, got.InRepository())
	assert.True(t, busy, "the repository lock is held while the step sees the repository")
}

// A global-scope step is never shown the repository.
func TestEnsureGlobalStepNeverSeesRepository(t *testing.T) {
	r := newRig(t, true)
	var got Env
	r.opts.steps = []Step{{Name: "g", Scope: ScopeGlobal, Run: func(_ context.Context, e Env) (Outcome, error) {
		got = e
		return Outcome{}, nil
	}}}
	r.opts.order = []string{"g"}
	r.ensure()
	assert.False(t, got.InRepository())
	assert.Equal(t, r.global, got.Global)
}

// While an abandoned step runs, its Ensure still holds the repair lock, so the
// next Ensure defers and starts no second instance; once the step returns, the
// next Ensure runs it again.
func TestEnsureAbandonedStepDefersNextEnsure(t *testing.T) {
	r := newRig(t, false)
	r.opts.stepBudget = 50 * time.Millisecond
	r.opts.lockWait = 50 * time.Millisecond
	var live, maxLive, starts atomic.Int32
	release := make(chan struct{})
	r.opts.steps = []Step{{Name: "hang", Scope: ScopeGlobal, Run: func(context.Context, Env) (Outcome, error) {
		starts.Add(1)
		n := live.Add(1)
		for {
			m := maxLive.Load()
			if n <= m || maxLive.CompareAndSwap(m, n) {
				break
			}
		}
		<-release
		live.Add(-1)
		return Outcome{}, nil
	}}}
	r.opts.order = []string{"hang"}
	first := r.ensureAsync()
	require.Eventually(t, func() bool { return starts.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	r.ensure()
	assert.EqualValues(t, 1, maxLive.Load(), "two instances of one step ran at once")
	assert.EqualValues(t, 1, starts.Load())
	_, err := r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist, "a deferred step withholds the stamp")

	close(release)
	<-first
	require.Eventually(t, func() bool { return live.Load() == 0 }, time.Second, 5*time.Millisecond)
	// The marker is released when the goroutine returns: the step runs again.
	var again atomic.Bool
	r.opts.steps[0].Run = func(context.Context, Env) (Outcome, error) {
		again.Store(true)
		return Outcome{}, nil
	}
	require.Eventually(t, func() bool { r.ensure(); return again.Load() }, 2*time.Second, 20*time.Millisecond)
}

// A cancelled context runs nothing and creates no lock, log, or directory.
func TestEnsureCancelledContextTouchesNothing(t *testing.T) {
	r := newRig(t, true)
	r.step("s", ScopeBoth, Outcome{Items: 1}, nil)
	require.NoError(t, os.MkdirAll(r.global, 0o700))
	gb, rb := treeOf(t, r.global), treeOf(t, r.store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := ensure(ctx, r.roots, r.global, r.opts)
	require.NoError(t, err)
	assert.Equal(t, Result{}, res)
	assert.Empty(t, r.ran)
	assert.Equal(t, gb, treeOf(t, r.global), "global untouched")
	assert.Equal(t, rb, treeOf(t, r.store), "repository untouched")

	res, err = cheapPass(ctx, []string{"s"}, r.roots, r.global, r.opts)
	require.NoError(t, err)
	assert.Equal(t, Result{}, res)
	assert.Equal(t, gb, treeOf(t, r.global), "a cheap pass with a cancelled context touches nothing")
}

// A ScopeBoth residual lands in the stamp of the store it belongs to: the
// repository stamp when the step names the repository or, unnamed, ran for it.
func TestEnsureScopeBothResidualLandsInItsOwnStamp(t *testing.T) {
	r := newRig(t, true)
	repoRes := Residual{Step: "both", ID: "repo-thing", Reason: "repo condition", Scope: ScopeRepository}
	globalRes := Residual{Step: "both", ID: "global-thing", Reason: "global condition", Scope: ScopeGlobal}
	unnamed := Residual{Step: "both", ID: "unnamed", Reason: "unnamed"}
	r.step("both", ScopeBoth, Outcome{Residuals: []Residual{repoRes, globalRes, unnamed}}, nil)
	r.ensure()
	g, err := r.globalStamp()
	require.NoError(t, err)
	rs, err := r.repoStamp()
	require.NoError(t, err)
	ids := func(rr []Residual) []string {
		var out []string
		for _, x := range rr {
			out = append(out, x.ID)
		}
		return out
	}
	assert.Equal(t, []string{"global-thing"}, ids(g.Residual))
	assert.Equal(t, []string{"repo-thing", "unnamed"}, ids(rs.Residual))
}
