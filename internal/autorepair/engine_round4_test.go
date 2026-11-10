package autorepair

import (
	"context"
	"errors"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// A step name becomes a lock file name; one with a separator would leave
// <global>/locks.
func TestRegisterRejectsUnsafeStepName(t *testing.T) {
	saved := Steps
	t.Cleanup(func() { Steps = saved })
	run := func(context.Context, Env) (Outcome, error) { return Outcome{}, nil }
	for _, name := range []string{"../x", "../../../x", "a/b", "", "Upper", "a b", "a.b"} {
		assert.Panics(t, func() { Register(Step{Name: name, Scope: ScopeGlobal, Run: run}) }, "name %q", name)
	}
	assert.NotPanics(t, func() { Register(Step{Name: "ok-1", Scope: ScopeGlobal, Run: run}) })
}

func TestStepOrderNamesAreSafe(t *testing.T) {
	re := regexp.MustCompile(`^[a-z0-9-]+$`)
	for _, n := range StepOrder {
		assert.Regexp(t, re, n)
	}
}

// HookEnv writes nothing, a located-with-error repository included: the skip
// line belongs to CheapPass, which holds the global lock.
func TestHookEnvWritesNothing(t *testing.T) {
	g := physicalTemp(t) + "/global"
	plain := physicalTemp(t)
	require.NoError(t, os.MkdirAll(g, 0o700))
	before := treeOf(t, g)
	pb := treeOf(t, plain)
	env, err := HookEnv(resolve.Located{Store: plain, Checkout: plain}, g)
	require.NoError(t, err)
	assert.False(t, env.InRepository())
	assert.Equal(t, before, treeOf(t, g))
	assert.Equal(t, pb, treeOf(t, plain))

	_, err = HookEnv(resolve.Located{Store: plain, StoreErr: errors.New("boom")}, g)
	require.NoError(t, err)
	assert.Equal(t, before, treeOf(t, g), "HookEnv wrote the log on a locate error")
}
