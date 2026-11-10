package autorepair

import (
	"context"
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

func TestPreCommitUntracksRecordTreeTrace(t *testing.T) {
	dir := damageRepo(t, "tracked-trace-under-sessions")
	saved := Steps
	t.Cleanup(func() { Steps = saved })
	Steps = nil
	Register(Step{Name: "untrack-local", Scope: ScopeRepository, Run: untrackLocal})

	line, err := PreCommit(context.Background(), untrackEnv(dir, &recLog{}))
	require.NoError(t, err)
	assert.Empty(t, line, "record-tree trace is untracked without a line")
	assert.Len(t, stagedDeletions(t, dir), 2)
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
