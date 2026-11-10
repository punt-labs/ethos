package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreCommitUnstagesLocalAndRunsNamedSteps(t *testing.T) {
	dir := damageRepo(t, "force-added-local-file")
	var ran []string
	saved := Steps
	t.Cleanup(func() { Steps = saved })
	Steps = []Step{
		{Name: "live-tails", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) { ran = append(ran, "live-tails"); return Outcome{}, nil }},
		{Name: "global-missions", Scope: ScopeBoth, Run: func(context.Context, Env) (Outcome, error) {
			ran = append(ran, "global-missions")
			return Outcome{}, nil
		}},
		{Name: "hook-sections", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) { ran = append(ran, "hook-sections"); return Outcome{}, nil }},
	}

	line, err := PreCommit(context.Background(), untrackEnv(dir, &recLog{}))
	require.NoError(t, err)
	assert.Equal(t, "ethos: hook pre-commit: unstaged 1 path(s) under .punt-labs/local/; they stay on disk", line)
	assert.Equal(t, []string{"live-tails", "hook-sections"}, ran, "only the cheap-pass steps run")
	assert.Empty(t, gitIn(t, dir, "ls-files", ".punt-labs/local"))
	assert.FileExists(t, filepath.Join(dir, ".punt-labs/local/ethos/sessions/sess-damage-0001.audit.jsonl"))

	line, err = PreCommit(context.Background(), untrackEnv(dir, &recLog{}))
	require.NoError(t, err)
	assert.Empty(t, line, "nothing to unstage prints nothing")
}

func TestPreCommitOutsideRepository(t *testing.T) {
	line, err := PreCommit(context.Background(), Env{})
	require.NoError(t, err)
	assert.Empty(t, line)
}

func TestPreCommitStepFailureDoesNotFail(t *testing.T) {
	saved := Steps
	t.Cleanup(func() { Steps = saved })
	Steps = []Step{{Name: "live-tails", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) {
		return Outcome{}, assert.AnError
	}}}
	l := &recLog{}
	dir := damageRepo(t, "force-added-local-file")
	_, err := PreCommit(context.Background(), untrackEnv(dir, l))
	require.NoError(t, err)
	assert.Contains(t, l.actions, "live-tails failed ")
}

func TestPreCommitStepsAreInStepOrder(t *testing.T) {
	for _, name := range preCommitSteps {
		assert.Contains(t, StepOrder, name, "a misspelled pre-commit step is skipped silently")
	}
}

func TestPreCommitLogsUnregisteredStep(t *testing.T) {
	saved := Steps
	t.Cleanup(func() { Steps = saved })
	Steps = nil
	l := &recLog{}
	dir := damageRepo(t, "force-added-local-file")
	_, err := PreCommit(context.Background(), untrackEnv(dir, l))
	require.NoError(t, err)
	assert.Contains(t, l.actions, "live-tails skipped: step not registered ")
}

func TestPreCommitUnstagesWithoutAStoreAndLogsTheSkip(t *testing.T) {
	dir := damageRepo(t, "force-added-local-file")
	saved := Steps
	t.Cleanup(func() { Steps = saved })
	ran := false
	Steps = []Step{{Name: "live-tails", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) { ran = true; return Outcome{}, nil }}}
	l := &recLog{}
	env := untrackEnv(dir, l)
	env.Roots.Store = ""

	line, err := PreCommit(context.Background(), env)
	require.NoError(t, err)
	assert.NotEmpty(t, line, "local paths are still unstaged")
	assert.Empty(t, gitIn(t, dir, "ls-files", ".punt-labs/local"))
	assert.False(t, ran, "no repair step runs without a store")
	assert.Contains(t, l.actions, "pre-commit skipped repair steps: store not located ")
}

func TestPreCommitStepFailureLeavesALogLine(t *testing.T) {
	saved := Steps
	t.Cleanup(func() { Steps = saved })
	Steps = []Step{{Name: "live-tails", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) {
		return Outcome{}, assert.AnError
	}}}
	global := t.TempDir()
	dir := damageRepo(t, "force-added-local-file")
	_, err := PreCommit(context.Background(), untrackEnv(dir, NewAppendLog(global, dir, dir, "test")))
	require.NoError(t, err)
	b, err := os.ReadFile(LogPath(global))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"step":"live-tails"`)
	assert.Contains(t, string(b), `"action":"failed"`)
}
