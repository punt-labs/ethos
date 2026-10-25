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

// A git failure is never read as an absent value: an unset key and no remote
// are absent; "dubious ownership" and a corrupt config are errors.
func TestGitReadsClassifyFailures(t *testing.T) {
	cases := map[string]struct {
		body    string
		cfgWant string
		cfgErr  bool
		repoErr bool
	}{
		"unset key / no remote": {body: "[ \"$1\" = remote ] && { echo 'error: No such remote' >&2; exit 2; }\nexit 1\n"},
		"dubious ownership":     {body: "echo 'fatal: detected dubious ownership' >&2\nexit 128\n", cfgErr: true, repoErr: true},
		"corrupt config":        {body: "echo 'fatal: bad config line 3' >&2\nexit 128\n", cfgErr: true, repoErr: true},
		"not a repository":      {body: "[ \"$1\" = remote ] && { echo 'fatal: not a git repository (or any of the parent directories): .git' >&2; exit 128; }\nexit 1\n"},
		"value":                 {body: "echo ' v '\n", cfgWant: "v"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fakeGit(t, c.body)
			t.Chdir(t.TempDir())
			v, err := GitConfig("user.name")
			if c.cfgErr {
				require.Error(t, err)
				assert.Empty(t, v)
			} else {
				require.NoError(t, err)
				assert.Equal(t, c.cfgWant, v)
			}
			r, err := RepoName()
			if c.repoErr {
				require.Error(t, err)
				assert.Empty(t, r)
			} else if name != "value" {
				require.NoError(t, err)
				assert.Empty(t, r)
			}
		})
	}
}

// No git on PATH: identity resolution does not require git, so GitConfig is
// unset; RepoName needs git and says so.
func TestGitMissingFromPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())
	v, err := GitConfig("user.name")
	require.NoError(t, err)
	assert.Empty(t, v)
	_, err = RepoName()
	require.Error(t, err)
	assert.ErrorIs(t, err, exec.ErrNotFound)
}
