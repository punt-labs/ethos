package autorepair

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hang is an abandoned-step fixture: step "a" ignores its budget and runs until
// release is closed; step "b" records whether it ran while "a" was live.
type hang struct {
	started      chan struct{}
	release      chan struct{}
	aLive        atomic.Bool
	aStarts      atomic.Int32
	bRuns        atomic.Int32
	bDuring      atomic.Bool
	aSawLockFree atomic.Bool
}

func (r *rig) hangSteps(t *testing.T, scope Scope) *hang {
	h := &hang{started: make(chan struct{}, 4), release: make(chan struct{})}
	var p = func() func() (func(), error) {
		_, pol := repositoryTarget(r.roots)
		return func() (func(), error) { return repositoryLock(pol, 0) }
	}()
	r.opts.steps = []Step{
		{Name: "a", Scope: scope, Run: func(context.Context, Env) (Outcome, error) {
			h.aStarts.Add(1)
			h.aLive.Store(true)
			h.started <- struct{}{}
			<-h.release
			if r.roots.Store != "" {
				if rel, err := p(); err == nil {
					rel()
					h.aSawLockFree.Store(true)
				}
			}
			h.aLive.Store(false)
			return Outcome{}, nil
		}},
		{Name: "b", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) {
			h.bRuns.Add(1)
			if h.aLive.Load() {
				h.bDuring.Store(true)
			}
			return Outcome{}, nil
		}},
	}
	r.opts.order = []string{"a", "b"}
	return h
}

// ensureAsync runs one Ensure in its own goroutine; the returned channel closes
// when it has returned.
func (r *rig) ensureAsync() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ensure(context.Background(), r.roots, r.global, r.opts)
	}()
	return done
}

func waitStarted(t *testing.T, h *hang) {
	t.Helper()
	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("step a never started")
	}
	time.Sleep(200 * time.Millisecond) // past the 50ms step budget: a is abandoned
}

// An abandoned step keeps its Env, so its Ensure keeps both repair locks until
// the step returns, and no later step runs in that pass.
func TestAbandonedStepOutlivesRepositoryLock(t *testing.T) {
	r := newRig(t, true)
	r.opts.stepBudget = 50 * time.Millisecond
	_, p := repositoryTarget(r.roots)
	h := r.hangSteps(t, ScopeBoth)
	done := r.ensureAsync()
	waitStarted(t, h)

	rel, err := repositoryLock(p, 0)
	if err == nil {
		rel()
	}
	assert.Error(t, err, "the repository lock was free while the abandoned step still ran")
	assert.EqualValues(t, 0, h.bRuns.Load(), "a step ran after an abandoned one in the same pass")

	close(h.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not return after the abandoned step did")
	}
	assert.False(t, h.aSawLockFree.Load(), "the repository lock was free while the abandoned step still ran")
	assert.False(t, h.bDuring.Load())
	_, serr := r.globalStamp()
	assert.ErrorIs(t, serr, os.ErrNotExist, "a pass stopped by an abandoned step stamped the store")
}

// While an abandoned step runs, the next Ensure defers at the lock and
// converts nothing.
func TestAbandonedStepBlocksNextEnsure(t *testing.T) {
	r := newRig(t, true)
	r.opts.stepBudget = 50 * time.Millisecond
	r.opts.lockWait = 50 * time.Millisecond
	h := r.hangSteps(t, ScopeBoth)
	first := r.ensureAsync()
	waitStarted(t, h)

	res, err := ensure(context.Background(), r.roots, r.global, r.opts)
	require.NoError(t, err)
	assert.Equal(t, Result{}, res)
	assert.EqualValues(t, 1, h.aStarts.Load(), "a second Ensure started the step again")
	assert.EqualValues(t, 0, h.bRuns.Load())
	_, serr := r.globalStamp()
	assert.ErrorIs(t, serr, os.ErrNotExist)

	close(h.release)
	<-first
}

// Once the abandoned step returns, the locks are released and the next Ensure
// proceeds.
func TestLocksReleaseWhenAbandonedStepReturns(t *testing.T) {
	r := newRig(t, true)
	r.opts.stepBudget = 50 * time.Millisecond
	h := r.hangSteps(t, ScopeBoth)
	first := r.ensureAsync()
	waitStarted(t, h)
	close(h.release)
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not return after the abandoned step did")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.ensure() }()
	wg.Wait()
	assert.EqualValues(t, 2, h.aStarts.Load(), "the next Ensure did not run the step")
	assert.EqualValues(t, 1, h.bRuns.Load())
	_, serr := r.globalStamp()
	require.NoError(t, serr)
}

// TestEnsureDoesNotWaitUnboundedlyForAbandonedStep: a step that ignores its
// context is abandoned at its budget, and Ensure stops waiting for it once the
// Ensure budget is spent. The step keeps its lock while its goroutine lives.
func TestEnsureDoesNotWaitUnboundedlyForAbandonedStep(t *testing.T) {
	r := newRig(t, true)
	r.opts.stepBudget = 50 * time.Millisecond
	r.opts.ensureBudget = 300 * time.Millisecond
	h := r.hangSteps(t, ScopeRepository)
	// Release the hung step and wait for its goroutine before the test ends:
	// the temporary directory is removed at cleanup, and a goroutine still
	// writing under it makes that removal fail on Linux.
	t.Cleanup(func() {
		close(h.release)
		deadline := time.Now().Add(5 * time.Second)
		for h.aLive.Load() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	start := time.Now()
	done := r.ensureAsync()
	select {
	case <-done:
	case <-time.After(r.opts.ensureBudget + time.Second):
		t.Fatal("Ensure still waiting for the abandoned step after EnsureBudget+1s")
	}
	assert.GreaterOrEqual(t, time.Since(start), r.opts.stepBudget)
	assert.True(t, h.aLive.Load(), "the abandoned step is still running")
	release, err := acquire(stepLockPath(r.global, "a"), 0)
	if err == nil {
		release()
	}
	assert.ErrorIs(t, err, errBusy, "the step lock is held while the goroutine lives")
}
