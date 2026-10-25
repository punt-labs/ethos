package gitenv_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	testhelpers.MustIsolateGit()
	os.Exit(m.Run())
}

// A .git git cannot use is not "outside a repository": Read reports an error
// for a gitfile naming a missing directory and for a worktree whose main
// checkout is gone, whatever the caller said absent means.
func TestReadBrokenGitDirIsAnError(t *testing.T) {
	all := gitenv.AbsentUnset | gitenv.AbsentNoRemote | gitenv.AbsentNotRepo
	for name, dir := range testhelpers.BrokenGitDirs(t) {
		t.Run(name, func(t *testing.T) {
			_, absent, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: dir, Env: gitenv.Parsed(), Args: []string{"remote", "get-url", "origin"}}, all)
			require.Error(t, err)
			assert.False(t, absent)
		})
	}
}

// A message that says "not a git repository" without "(or any of the parent
// directories)" is git refusing a .git it found, not finding none.
func TestNotARepoMatcher(t *testing.T) {
	assert.True(t, gitenv.IsNotARepo("fatal: not a git repository (or any of the parent directories): .git"))
	assert.False(t, gitenv.IsNotARepo("fatal: not a git repository: /nonexistent/x"))
	assert.False(t, gitenv.IsNotARepo("fatal: detected dubious ownership"))
}

// Trace output on stderr must not turn "unset" into a failure: Parsed and
// Inherited strip the variables that make git write to it.
func TestTraceVariablesDoNotTurnUnsetIntoFailure(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(cfg, nil, 0o644))
	for _, v := range []string{"GIT_TRACE", "GIT_TRACE2", "GIT_TRACE2_EVENT", "GIT_TRACE_SETUP", "GIT_TRACE_PERFORMANCE", "GIT_CURL_VERBOSE"} {
		t.Setenv(v, "1")
	}
	for name, env := range map[string]func() []string{"Parsed": gitenv.Parsed, "Inherited": gitenv.Inherited} {
		t.Run(name, func(t *testing.T) {
			_, absent, err := gitenv.Read(gitenv.Spec{
				Git: "git", Env: env(), Args: []string{"config", "--file", cfg, "user.name"},
			}, gitenv.AbsentUnset)
			require.NoError(t, err)
			assert.True(t, absent)
		})
	}
}
