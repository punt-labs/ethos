package autorepair

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// stoppedPass runs an Ensure whose first step (scope first) is abandoned, so
// the pass stops before the second step (scope second). It returns the rig,
// the second step's run counter, and the first step's run counter.
func stoppedPass(t *testing.T, first, second Scope) (*rig, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	r := newRig(t, true)
	r.opts.stepBudget = 50 * time.Millisecond
	release := make(chan struct{})
	var aRuns, bRuns atomic.Int32
	r.opts.steps = []Step{
		{Name: "a", Scope: first, Run: func(context.Context, Env) (Outcome, error) {
			if aRuns.Add(1) == 1 {
				<-release
			}
			return Outcome{}, nil
		}},
		{Name: "b", Scope: second, Run: func(context.Context, Env) (Outcome, error) { bRuns.Add(1); return Outcome{}, nil }},
	}
	r.opts.order = []string{"a", "b"}
	done := r.ensureAsync()
	time.Sleep(300 * time.Millisecond)
	close(release)
	<-done
	return r, &aRuns, &bRuns
}

// A pass that stops after an abandoned step writes no stamp of any scope, and
// the next Ensure runs every step.
func TestEnsureStoppedPassWritesNoStamp(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second Scope
	}{
		{"repository step abandons, global skipped", ScopeRepository, ScopeGlobal},
		{"global step abandons, repository skipped", ScopeGlobal, ScopeRepository},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, aRuns, bRuns := stoppedPass(t, tc.first, tc.second)
			assert.EqualValues(t, 0, bRuns.Load(), "b ran after the stop")
			_, err := r.globalStamp()
			assert.ErrorIs(t, err, os.ErrNotExist, "global stamp")
			_, err = r.repoStamp()
			assert.ErrorIs(t, err, os.ErrNotExist, "repository stamp")

			r.ensure()
			assert.EqualValues(t, 2, aRuns.Load(), "a runs again")
			assert.EqualValues(t, 1, bRuns.Load(), "b runs on the next Ensure")
			_, err = r.globalStamp()
			assert.NoError(t, err)
			_, err = r.repoStamp()
			assert.NoError(t, err)
		})
	}
}

// A step that panics after its budget has passed leaves one log line naming
// it: its result goes to a channel nobody reads, so the log is the only trace.
func TestAbandonedStepLateResultIsLogged(t *testing.T) {
	r := newRig(t, false)
	r.opts.stepBudget = 50 * time.Millisecond
	release := make(chan struct{})
	r.opts.steps = []Step{{Name: "a", Scope: ScopeGlobal, Run: func(context.Context, Env) (Outcome, error) {
		<-release
		panic("late kaboom")
	}}}
	r.opts.order = []string{"a"}
	done := r.ensureAsync()
	time.Sleep(300 * time.Millisecond)
	close(release)
	<-done
	n := 0
	for _, l := range repairLogLines(t, r.global) {
		if strings.Contains(l, "late result for abandoned step a") && strings.Contains(l, "late kaboom") {
			n++
		}
	}
	assert.Equal(t, 1, n, "one late-result line; log:\n%s", strings.Join(repairLogLines(t, r.global), "\n"))
}
