package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ResolveRepo reports a git that does not answer as an error; a checkout with
// no origin is ("", nil).
func TestResolveRepoClassifiesFailures(t *testing.T) {
	t.Run("timeout is an error", func(t *testing.T) {
		testhelpers.HangingGit(t)
		t.Chdir(t.TempDir())
		got, err := ResolveRepo(".")
		require.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.Empty(t, got)
	})
	t.Run("no origin is absent", func(t *testing.T) {
		dir := t.TempDir()
		cmd := testhelpers.GitCommand("-C", dir, "init", "-q")
		require.NoError(t, cmd.Run())
		t.Chdir(dir)
		got, err := ResolveRepo(".")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

// A roster written while the repository lookup failed records
// repo_unresolved with the reason and no Repo; the notice names the cause. A
// no-origin checkout still records Repo "" and nothing else.
func TestCreateSessionRosterRepoLookupFailure(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, testhelpers.GitCommand("-C", repo, "init", "-q").Run())
	t.Chdir(repo)
	t.Setenv("USER", "someone")

	t.Run("failure", func(t *testing.T) {
		testhelpers.ShimGit(t, "remote")
		ss := session.NewStore(t.TempDir())
		var notice string
		testhelpers.CaptureStderr(t, func() {
			notice = createSessionRoster(ss, "s-fail", &identity.Identity{Handle: "mal"}, nil, "claude")
		})
		r, err := ss.Load("s-fail")
		require.NoError(t, err)
		assert.Empty(t, r.Repo)
		assert.Contains(t, r.RepoUnresolved, "did not answer")
		assert.Contains(t, notice, "did not answer")
	})
	t.Run("no origin", func(t *testing.T) {
		ss := session.NewStore(t.TempDir())
		notice := createSessionRoster(ss, "s-none", &identity.Identity{Handle: "mal"}, nil, "claude")
		r, err := ss.Load("s-none")
		require.NoError(t, err)
		assert.Empty(t, r.Repo)
		assert.Empty(t, r.RepoUnresolved)
		assert.Empty(t, notice)
	})
}

// A failed human-identity resolution records no persona for the human (not
// the OS user's) and says why; "no identity matched" keeps the OS-user
// fallback.
func TestCreateSessionRosterHumanFailure(t *testing.T) {
	t.Setenv("ETHOS_REPO_ROOT", t.TempDir())
	t.Setenv("USER", "someone")
	ss := session.NewStore(t.TempDir())

	notice := createSessionRoster(ss, "s-h-fail", nil, assertErr("git did not answer"), "claude")
	r, err := ss.Load("s-h-fail")
	require.NoError(t, err)
	require.NotEmpty(t, r.Participants)
	assert.Equal(t, "someone", r.Participants[0].AgentID)
	assert.Empty(t, r.Participants[0].Persona, "a failure is not recorded as the OS user's persona")
	assert.Contains(t, notice, "git did not answer")

	createSessionRoster(ss, "s-h-none", nil, nil, "claude")
	r, err = ss.Load("s-h-none")
	require.NoError(t, err)
	assert.Equal(t, "someone", r.Participants[0].Persona, "no identity matched: the OS user is the fallback")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

// HandleSessionStart puts the cause of a failed lookup in the session's
// additionalContext.
func TestHandleSessionStartNamesLookupFailure(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, testhelpers.GitCommand("-C", repo, "init", "-q").Run())
	t.Chdir(repo)
	t.Setenv("USER", "someone")
	testhelpers.ShimGit(t, "remote")
	dir := t.TempDir()
	store := identity.NewStore(dir)
	ss := session.NewStore(dir)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })
	herr := HandleSessionStart(strings.NewReader(`{"session_id":"s-ctx"}`), SessionStartDeps{Store: store, Sessions: ss})
	require.NoError(t, w.Close())
	os.Stdout = orig
	require.NoError(t, herr)
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	var res SessionStartResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &res), buf.String())
	assert.Contains(t, res.HookSpecificOutput.AdditionalContext, "did not answer")
	assert.Contains(t, res.SystemMessage, "did not answer")
}

// A .git git cannot use is a failure for ResolveRepo, so the roster records
// repo_unresolved rather than Repo "".
func TestResolveRepoBrokenGitDirIsAnError(t *testing.T) {
	for name, dir := range testhelpers.BrokenGitDirs(t) {
		t.Run(name, func(t *testing.T) {
			t.Chdir(dir)
			got, err := ResolveRepo(".")
			require.Error(t, err)
			assert.Empty(t, got)
		})
	}
}

// The repository identity is read with `git -C <checkout>` once the checkout
// is known, as audit.RepoIdentity does: a session started from a directory
// outside any repository, with ETHOS_REPO_ROOT naming one, records that
// repository's origin.
func TestCreateSessionRosterReadsTheOriginOfTheCheckout(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, testhelpers.GitCommand("-C", repo, "init", "-q").Run())
	require.NoError(t, testhelpers.GitCommand("-C", repo, "remote", "add", "origin", "git@github.com:punt-labs/probe.git").Run())
	t.Setenv("ETHOS_REPO_ROOT", repo)
	t.Chdir(t.TempDir()) // outside any repository (the temp root is the git ceiling)

	ss := session.NewStore(t.TempDir())
	notice := createSessionRoster(ss, "s-origin", &identity.Identity{Handle: "mal"}, nil, "claude")
	r, err := ss.Load("s-origin")
	require.NoError(t, err)
	assert.Equal(t, "punt-labs/probe", r.Repo)
	assert.Empty(t, r.RepoUnresolved)
	assert.Empty(t, notice)
}

// A payload with no session_id writes no roster, and the session is told so;
// a roster that cannot be written is told too.
func TestCreateSessionRosterReportsWhatItCouldNotDo(t *testing.T) {
	t.Setenv("ETHOS_REPO_ROOT", t.TempDir())
	t.Run("no session_id", func(t *testing.T) {
		notice := createSessionRoster(session.NewStore(t.TempDir()), "", nil, nil, "claude")
		assert.Contains(t, notice, "session_id")
	})
	t.Run("roster write fails", func(t *testing.T) {
		dir := t.TempDir()
		// A file where the sessions directory goes: MkdirAll fails.
		require.NoError(t, os.WriteFile(filepath.Join(dir, "sessions"), []byte("x"), 0o644))
		var notice string
		testhelpers.CaptureStderr(t, func() {
			notice = createSessionRoster(session.NewStore(dir), "s-nowrite", nil, nil, "claude")
		})
		assert.Contains(t, notice, "roster could not be written")
	})
}
