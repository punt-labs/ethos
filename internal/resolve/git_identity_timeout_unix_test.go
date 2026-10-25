//go:build linux || darwin

package resolve

import (
	"os/exec"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangingGit puts a git on PATH that never answers, and shortens the
// deadline so the test does not wait gitenv.Timeout.
func hangingGit(t *testing.T) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	fakeGit(t, "exec "+sleep+" 60\n")
	testhelpers.GitTimeout(t, 200*time.Millisecond)
	t.Chdir(t.TempDir())
}

// A git config read that does not answer is an error naming git and the
// directory, not an unset key.
func TestGitConfigTimeout(t *testing.T) {
	hangingGit(t)
	var v string
	var err error
	within(t, 10*time.Second, func() { v, err = GitConfig("user.name") })
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.Empty(t, v)
	assert.Contains(t, err.Error(), "user.name")
}

// A remote read that does not answer is an error, not "no origin".
func TestRepoNameTimeout(t *testing.T) {
	hangingGit(t)
	var v string
	var err error
	within(t, 10*time.Second, func() { v, err = RepoName() })
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.Empty(t, v)
}

// Resolve does not answer from $USER when the git identity read hung: a
// hang is an error, not an absent git user.
func TestResolveGitTimeoutIsAnError(t *testing.T) {
	t.Setenv("USER", "mal")
	s := testStoreWithIdentity(t, &identity.Identity{Name: "Mal Reynolds", Handle: "mal", Kind: "human"})
	hangingGit(t)
	var handle string
	var err error
	within(t, 10*time.Second, func() { handle, err = Resolve(s, nil) })
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.Empty(t, handle)
}
