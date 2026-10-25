//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangGit puts a git that never answers first on PATH and shortens the
// deadline for the test.
func hangGit(t *testing.T) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nexec "+sleep+" 60\n"), 0o755))
	t.Setenv("PATH", dir)
	testhelpers.GitTimeout(t, 200*time.Millisecond)
}

// team for-repo with no argument reports a git that hung reading the
// remote, on the same error path as "no git remote".
func TestTeamForRepoGitTimeout(t *testing.T) {
	t.Chdir(t.TempDir())
	hangGit(t)
	err := runTeamForRepo(&cobra.Command{}, "")
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.Contains(t, err.Error(), "could not determine repo name")
}

// setup --file with no email reports a git that hung reading user.email,
// on the same error path as an unset user.email.
func TestSetupFileGitTimeout(t *testing.T) {
	_, repo := setupTestEnv(t)
	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, "name: Test User\nsolo: true\n")
	setupFile = cfgPath
	testhelpers.ShimGit(t, "config") // locating the repository works; reading the config hangs
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	err := runSetup(cmd)
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.Contains(t, err.Error(), "user.email")
}

// Interactive setup offers no email default when the git read hung, and
// says why on stderr rather than leaving the prompt silently empty.
func TestSetupInteractiveGitTimeout(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString("Test User\n\nme@example.com\n\n\n\n\n\n")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; _ = r.Close() })
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	hangGit(t)

	var errb bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&errb)
	cfg, err := setupInteractive(cmd)
	require.NoError(t, err)
	assert.Equal(t, "me@example.com", cfg.Email)
	assert.Contains(t, errb.String(), "did not answer")
	assert.Contains(t, errb.String(), "Email: ", "no default is offered")
}

// The vacuum cross-check's session scan says on its error stream that the
// repository identity could not be read, rather than matching sessions with
// an empty one.
func TestActiveRepoSessionsGitTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	testhelpers.HangingGit(t)
	var errb bytes.Buffer
	got := activeRepoSessions(t.TempDir(), &errb)
	assert.Nil(t, got)
	assert.Contains(t, errb.String(), "did not answer")
}

// mission --verify reports a git that did not answer as the error, naming
// the timeout, for both of its git commands.
func TestVerifyResultAgainstNumstatGitTimeout(t *testing.T) {
	checkout := t.TempDir()
	testhelpers.HangingGit(t)
	err := verifyResultAgainstNumstat(&mission.Result{}, "main", checkout)
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.NotContains(t, err.Error(), "invalid base ref", "a git that did not answer is not a bad ref")
}

// team add-bundle --apply refuses a clone that does not finish within
// bundleCloneTimeout, with the same message shape as every git timeout, and
// the production bound is five minutes.
func TestTeamAddBundleCloneTimeout(t *testing.T) {
	assert.Equal(t, 5*time.Minute, bundleCloneTimeout)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	testhelpers.HangingGit(t)
	prevLimit, prevApply, prevGlobal, prevName := cloneLimit, addBundleApply, addBundleGlobal, addBundleName
	cloneLimit, addBundleApply, addBundleGlobal, addBundleName = 200*time.Millisecond, true, true, ""
	t.Cleanup(func() {
		cloneLimit, addBundleApply, addBundleGlobal, addBundleName = prevLimit, prevApply, prevGlobal, prevName
	})
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := runTeamAddBundle(cmd, "https://example.invalid/team-bundle.git")
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.Contains(t, err.Error(), "clone")
	assert.Contains(t, err.Error(), "200ms")
}

// session purge returns the timeout when git does not answer reading this
// checkout's identity: no purge runs against an empty identity.
func TestSessionPurgeGitTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ETHOS_REPO_ROOT", t.TempDir())
	testhelpers.HangingGit(t)
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := runSessionPurge(cmd)
	require.ErrorIs(t, err, gitenv.ErrTimeout)
}

// audit seal fails closed when git does not answer, and says the seal could
// not run, where, and that nothing was sealed or staged and the commit is
// blocked with the tree unchanged.
func TestAuditSealGitTimeoutFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	t.Setenv("ETHOS_REPO_ROOT", repo)
	testhelpers.HangingGit(t)
	var out, errb bytes.Buffer
	err := runAuditSeal(&out, &errb)
	assert.ErrorIs(t, err, failClosed{})
	msg := errb.String()
	assert.Contains(t, msg, "the seal could not run because git did not answer")
	assert.Contains(t, msg, repo)
	assert.Contains(t, msg, "nothing was sealed or staged")
	assert.Contains(t, msg, "the commit is blocked and the tree is unchanged")
}

// The vacuum cross-check never matches a roster whose repository identity was
// unresolved against a no-origin checkout (identity ""): it skips it with a
// notice, while a roster that recorded "" for a no-origin checkout matches.
func TestActiveRepoSessionsSkipsUnresolvedRosters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ss := sessionStore()
	root := session.Participant{AgentID: "u"}
	primary := session.Participant{AgentID: "9999999", Parent: "u"}
	require.NoError(t, ss.CreateUnresolved("sess-un", root, primary, "git did not answer", "/co", "h"))
	require.NoError(t, ss.CreateInCheckout("sess-none", root, primary, "", "/co", "h"))
	fake := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "git"), []byte("#!/bin/sh\necho 'error: No such remote' >&2\nexit 2\n"), 0o755))
	t.Setenv("PATH", fake)

	var errb bytes.Buffer
	got := activeRepoSessions(t.TempDir(), &errb)
	var ids []string
	for _, a := range got {
		ids = append(ids, a.Session)
	}
	assert.Equal(t, []string{"sess-none"}, ids)
	assert.Contains(t, errb.String(), "sess-un")
	assert.Contains(t, errb.String(), "git did not answer")
}

// --verify separates a ref git does not know from every other git failure: the
// first is "invalid base ref"; the others name their cause and are never
// reported as a bad ref.
func TestVerifyDistinguishesAnInvalidRefFromOtherGitFailures(t *testing.T) {
	t.Chdir(t.TempDir())
	cases := map[string]struct {
		stderr  string
		invalid bool
	}{
		"bad revision":      {"fatal: bad revision 'nope'", true},
		"unknown revision":  {"fatal: ambiguous argument 'nope': unknown revision or path not in the working tree.", true},
		"single revision":   {"fatal: Needed a single revision", true},
		"dubious ownership": {"fatal: detected dubious ownership in repository at '/x'", false},
		"not a repository":  {"fatal: not a git repository (or any of the parent directories): .git", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho \""+c.stderr+"\" >&2\nexit 128\n"), 0o755))
			t.Setenv("PATH", bin)
			var err error
			testhelpers.CaptureStderr(t, func() { err = verifyResultAgainstNumstat(&mission.Result{}, "nope", t.TempDir()) })
			require.Error(t, err)
			if c.invalid {
				assert.Contains(t, err.Error(), "invalid base ref")
			} else {
				assert.NotContains(t, err.Error(), "invalid base ref")
				assert.Contains(t, err.Error(), c.stderr[len("fatal: "):])
			}
		})
	}
}

// --verify runs git in the located checkout, in the C locale, without the
// GIT_DIR a hook exports: a git that answers only to those is what it runs.
func TestVerifyResultAgainstNumstatRunsInTheCheckoutInTheCLocale(t *testing.T) {
	bin := t.TempDir()
	checkout := t.TempDir()
	script := "#!/bin/sh\n[ \"$LC_ALL\" = C ] || { echo \"fatal: LC_ALL=$LC_ALL\" >&2; exit 5; }\n" +
		"[ -z \"$GIT_DIR\" ] || { echo \"fatal: GIT_DIR=$GIT_DIR\" >&2; exit 5; }\n" +
		"[ \"$(pwd -P)\" = \"" + resolvedPath(t, checkout) + "\" ] || { echo \"fatal: ran in $(pwd -P)\" >&2; exit 5; }\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
	t.Setenv("PATH", bin)
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Chdir(t.TempDir())
	require.NoError(t, verifyResultAgainstNumstat(&mission.Result{}, "main", checkout))

	err := verifyResultAgainstNumstat(&mission.Result{}, "main", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside a git work tree")
}

func resolvedPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}
