package autorepair

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cheap runs the rig's steps as a cheap pass with the named steps.
func (r *rig) cheap(names ...string) Result {
	r.t.Helper()
	res, err := cheapPass(context.Background(), names, r.roots, r.global, r.opts)
	require.NoError(r.t, err)
	return res
}

// A cheap pass runs the named registered steps whatever the stamps say, logs
// under the global lock, and never writes a stamp.
func TestCheapPassRunsNamedStepsRegardlessOfStamp(t *testing.T) {
	r := newRig(t, true)
	r.step("live-tails", ScopeRepository, Outcome{Items: 2}, nil)
	r.step("untrack-local", ScopeRepository, Outcome{Items: 1}, nil)
	r.step("sessions", ScopeGlobal, Outcome{}, nil)
	r.ensure() // stamps are now current
	r.ran = nil

	res := r.cheap("live-tails", "sessions")
	assert.Equal(t, []string{"live-tails", "sessions"}, r.ran, "only the named steps, in StepOrder")
	assert.Equal(t, 2, res.Items, "the result has Ensure's shape")
	assert.True(t, res.Repaired)
	assert.Equal(t, "", Result{}.Line())

	lines := readLog(t, LogPath(r.global))
	var ran []string
	for _, l := range lines {
		if l["action"] == "ran" {
			ran = append(ran, l["step"].(string))
		}
	}
	assert.Contains(t, ran, "live-tails")
	assert.Contains(t, ran, "sessions")
}

func TestCheapPassNeverWritesAStamp(t *testing.T) {
	r := newRig(t, true)
	r.step("live-tails", ScopeRepository, Outcome{Items: 2}, nil)
	res := r.cheap("live-tails")
	assert.Equal(t, 2, res.Items)
	_, err := r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist, "no global stamp from a cheap pass")
	_, err = r.repoStamp()
	assert.ErrorIs(t, err, os.ErrNotExist, "no repository stamp from a cheap pass")
}

// A name that is not registered is skipped: a cheap pass is best effort and
// the unit that registers the step may not have landed.
func TestCheapPassSkipsUnregisteredAndUnnamedSteps(t *testing.T) {
	r := newRig(t, true)
	r.step("live-tails", ScopeRepository, Outcome{Items: 1}, nil)
	res := r.cheap("hook-sections", "live-tails")
	assert.Equal(t, []string{"live-tails"}, r.ran)
	assert.Equal(t, 1, res.Items)
}

// A busy global lock skips the pass: a hook never waits on a repair.
func TestCheapPassSkipsWhenLockBusy(t *testing.T) {
	r := newRig(t, false)
	r.step("sessions", ScopeGlobal, Outcome{Items: 1}, nil)
	release, err := acquire(r.opts.globalLockPath(r.global), time.Second)
	require.NoError(t, err)
	defer release()
	start := time.Now()
	res := r.cheap("sessions")
	assert.Less(t, time.Since(start), 500*time.Millisecond, "non-blocking")
	assert.Equal(t, Result{}, res)
	assert.Empty(t, r.ran)
}

// Outside a repository the repository-scoped names run nothing; the global
// ones still run. A plain repository (no .punt-labs) is left alone too.
func TestCheapPassRespectsScope(t *testing.T) {
	r := newRig(t, false)
	r.step("live-tails", ScopeRepository, Outcome{Items: 1}, nil)
	r.step("sessions", ScopeGlobal, Outcome{Items: 1}, nil)
	res := r.cheap("live-tails", "sessions")
	assert.Equal(t, []string{"sessions"}, r.ran)
	assert.Equal(t, 1, res.Items)
}

func TestCheapPassStepSeesEnvironment(t *testing.T) {
	r := newRig(t, true)
	var got Env
	r.opts.steps = []Step{{Name: "live-tails", Scope: ScopeRepository, Run: func(_ context.Context, e Env) (Outcome, error) {
		got = e
		return Outcome{}, nil
	}}}
	r.opts.order = []string{"live-tails"}
	r.cheap("live-tails")
	assert.True(t, got.InRepository())
	assert.Equal(t, r.global, got.Global)
	assert.NotEmpty(t, got.Policy.LocalRoot())
	assert.NotNil(t, got.Log)
}

func TestCheapPassNoGlobalRootIsNoop(t *testing.T) {
	res, err := CheapPass(context.Background(), []string{"sessions"}, Env{})
	require.NoError(t, err)
	assert.Equal(t, Result{}, res)
}

// HookEnv builds the Env Ensure shows a step, so a hook's pass matches
// Ensure's: same roots, policy, global root, version-tagged log.
func TestHookEnvEqualsEnsureEnv(t *testing.T) {
	r := newRig(t, true)
	var got Env
	r.opts.steps = []Step{{Name: "env", Scope: ScopeBoth, Run: func(_ context.Context, e Env) (Outcome, error) {
		got = e
		return Outcome{}, nil
	}}}
	r.opts.order = []string{"env"}
	r.ensure()
	want, err := hookEnv(r.roots, r.global, r.opts)
	require.NoError(t, err)
	assert.Equal(t, got.Roots, want.Roots)
	assert.Equal(t, got.Policy, want.Policy)
	assert.Equal(t, got.Global, want.Global)
	assert.Equal(t, got.Log, want.Log, "same log construction")
	assert.True(t, want.InRepository())

	outside := newRig(t, false)
	env, err := hookEnv(outside.roots, outside.global, outside.opts)
	require.NoError(t, err)
	assert.False(t, env.InRepository())
	assert.Equal(t, outside.global, env.Global)

	_, err = HookEnv(r.roots, "")
	assert.Error(t, err)
}

// A cheap pass with no named, registered step that serves a store takes no
// lock and writes no file, matching Ensure's zero-step rule.
func TestCheapPassWithNothingToRunLeavesTreesIdentical(t *testing.T) {
	r := newRig(t, true)
	r.step("sessions", ScopeGlobal, Outcome{Items: 1}, nil)
	require.NoError(t, os.MkdirAll(r.global, 0o700))
	for name, names := range map[string][]string{
		"unregistered name": {"hook-sections"},
		"unnamed step":      {"live-tails"},
	} {
		gb, rb := treeOf(t, r.global), treeOf(t, r.store)
		res := r.cheap(names...)
		assert.Equal(t, Result{}, res, name)
		assert.Equal(t, gb, treeOf(t, r.global), name+": global tree")
		assert.Equal(t, rb, treeOf(t, r.store), name+": repository tree")
	}

	out := newRig(t, false)
	out.step("live-tails", ScopeRepository, Outcome{Items: 1}, nil)
	require.NoError(t, os.MkdirAll(out.global, 0o700))
	gb := treeOf(t, out.global)
	assert.Equal(t, Result{}, out.cheap("live-tails"))
	assert.Equal(t, gb, treeOf(t, out.global), "repository step outside a repository")
}
