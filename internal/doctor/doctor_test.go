package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/punt-labs/ethos/v4/internal/agentmark"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/githook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/seed"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/punt-labs/ethos/v4/plugin/hooks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain strips CLAUDE_PID and CLAUDECODE before any test runs. This
// suite normally runs inside a real Claude Code session, where both are
// themselves set. DES-074 makes their presence the signal for "a session
// was expected, fail loud if unresolvable" — so leaving them ambient would
// make CheckHumanIdentity's "no session, fall back to git/OS" fixtures
// instead exercise the loud path. A test that wants the under-Claude-Code
// path sets one back with t.Setenv.
func TestMain(m *testing.M) {
	testhelpers.MustIsolateGit() // no inherited GIT_DIR; temp dirs stop at the temp root
	os.Unsetenv("CLAUDE_PID")
	os.Unsetenv("CLAUDECODE")
	// Stripping the env vars is not enough: a real claude process is
	// unavoidably this test binary's own process-tree ancestor whenever
	// this suite runs inside a live Claude Code session (as it normally
	// does), so process.UnderClaudeCode's ancestor-walk check would still
	// see it regardless. Force the "not under Claude Code" branch as this
	// binary's test default; a test that wants the loud, under-Claude-Code
	// path overrides this back locally with t.Cleanup.
	resolve.UnderClaudeCode = func() bool { return false }
	os.Exit(m.Run())
}

// newFixture builds an identity.Store and matching session.Store at a
// fresh temp root. The identities directory is created so identityDirStatus
// passes by default; individual tests remove it when they need a failure.
func newFixture(t *testing.T) (*identity.Store, *session.Store, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "identities", "seed.yaml"), []byte("name: Seed\nhandle: seed\nkind: agent\n"), 0o600))
	return identity.NewStore(root), session.NewStore(root), root
}

// outsideRepoTempDir creates a tempdir at /tmp so none of its ancestors
// contain a .git directory. Required for tests that must exercise the
// "not in a git repo" branch of CheckoutRootIfAny — t.TempDir() honors
// $TMPDIR, which is set to .tmp inside the ethos repo.
func outsideRepoTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ethos-doctor-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// writeIdentity marshals a minimal identity YAML to the store. It writes
// through the filesystem directly rather than Store.Save so tests can
// seed malformed or duplicate data that Save would reject.
func writeIdentity(t *testing.T, root, handle, body string) {
	t.Helper()
	p := filepath.Join(root, "identities", handle+".yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
}

func TestResultPassed(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"PASS", true},
		{"WARN", true}, // advisory — not a failure
		{"FAIL", false},
		{"", false},    // unknown status is not "passed"
		{"PAS", false}, // a typo must not read as passed
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			r := Result{Status: tc.status}
			assert.Equal(t, tc.want, r.Passed())
		})
	}
}

func TestPassedCountExcludesWarn(t *testing.T) {
	// A warned check must not be counted as both passed and a warning: strict
	// PassedCount excludes WARN, WarnCount counts it, and Passed/AllPassed keep
	// their advisory (WARN=not-failed) semantics.
	results := []Result{
		{Status: "PASS"}, {Status: "PASS"}, {Status: "WARN"}, {Status: "FAIL"},
	}
	assert.Equal(t, 2, PassedCount(results), "PASS-only count")
	assert.Equal(t, 1, WarnCount(results))
	assert.True(t, AnyFailed(results))
	assert.False(t, AllPassed(results)) // a FAIL is present
	// PassedCount + WarnCount + failures accounts for every result exactly once.
	assert.Equal(t, len(results), PassedCount(results)+WarnCount(results)+1)
}

func TestIdentityDirStatus(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		s, _, _ := newFixture(t)
		detail, status := identityDirStatus(s)
		assert.Equal(t, "PASS", status)
		assert.Equal(t, s.IdentitiesDir(), detail)
	})

	t.Run("missing", func(t *testing.T) {
		s, _, root := newFixture(t)
		require.NoError(t, os.RemoveAll(filepath.Join(root, "identities")))
		detail, status := identityDirStatus(s)
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "not found")
	})

	// A layered repo with no repo-local identities dir resolves from the global
	// store, so it passes when the global dir exists. Under repo-only nothing
	// else resolves, so it fails.
	layered := func(t *testing.T, globalHasIdentities, repoOnly bool) (identity.IdentityStore, string) {
		t.Helper()
		repoRoot := t.TempDir()   // no identities/ subdir
		globalRoot := t.TempDir() // identities/ only if asked
		if globalHasIdentities {
			require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(globalRoot, "identities", "g.yaml"), []byte("handle: g\n"), 0o600))
		}
		ls := identity.NewLayeredStoreWithBundle(identity.NewStore(repoRoot), nil, identity.NewStore(globalRoot), repoOnly)
		return ls, globalRoot
	}

	t.Run("layered, no repo dir, global dir passes naming it", func(t *testing.T) {
		ls, globalRoot := layered(t, true, false)
		detail, status := identityDirStatus(ls)
		assert.Equal(t, "PASS", status)
		assert.Equal(t, identity.NewStore(globalRoot).IdentitiesDir()+
			" (repo has none; layered resolution reads the global store)", detail)
	})

	t.Run("layered, neither dir is a fresh-install WARN", func(t *testing.T) {
		ls, _ := layered(t, false, false)
		res := identityDirResult(ls, nil)
		assert.Equal(t, "WARN", res.Status)
		assert.Contains(t, res.Detail, "ethos setup")
		assert.True(t, res.Passed(), "a fresh install must not gate doctor's exit")
	})

	t.Run("layered, empty repo and empty global dirs are a WARN", func(t *testing.T) {
		repoRoot, globalRoot := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "identities"), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o700))
		ls := identity.NewLayeredStoreWithBundle(identity.NewStore(repoRoot), nil, identity.NewStore(globalRoot), false)
		assert.Equal(t, "WARN", identityDirResult(ls, nil).Status)
	})

	t.Run("repo-only fresh install stays FAIL", func(t *testing.T) {
		ls := identity.NewLayeredStoreWithBundle(identity.NewStore(t.TempDir()), nil, identity.NewStore(t.TempDir()), true)
		assert.Equal(t, "FAIL", identityDirResult(ls, nil).Status)
	})

	t.Run("repo-only, no repo dir fails", func(t *testing.T) {
		ls, _ := layered(t, true, true)
		detail, status := identityDirStatus(ls)
		assert.Equal(t, "FAIL", status, "repo-only never reads the global store")
		assert.Contains(t, detail, "not found")
	})

	t.Run("layered, repo dir present is unchanged", func(t *testing.T) {
		repoRoot := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "identities"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "identities", "r.yaml"), []byte("handle: r\n"), 0o600))
		ls := identity.NewLayeredStore(identity.NewStore(repoRoot), identity.NewStore(t.TempDir()))
		detail, status := identityDirStatus(ls)
		assert.Equal(t, "PASS", status)
		assert.Equal(t, identity.NewStore(repoRoot).IdentitiesDir(), detail)
	})
}

func TestIdentityDirStatus_Hardening(t *testing.T) {
	notRoot := os.Geteuid() != 0 && runtime.GOOS != "windows"
	layered := func(repoRoot, globalRoot string, repoOnly bool) identity.IdentityStore {
		return identity.NewLayeredStoreWithBundle(identity.NewStore(repoRoot), nil, identity.NewStore(globalRoot), repoOnly)
	}
	populated := func(t *testing.T) string {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "identities", "g.yaml"), []byte("handle: g\n"), 0o600))
		return root
	}

	t.Run("regular file is not a directory", func(t *testing.T) {
		s, _, root := newFixture(t)
		require.NoError(t, os.RemoveAll(filepath.Join(root, "identities")))
		require.NoError(t, os.WriteFile(filepath.Join(root, "identities"), []byte("x"), 0o600))
		detail, status := identityDirStatus(s)
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "not a directory")
	})

	t.Run("empty dir fails with the reason", func(t *testing.T) {
		s, _, root := newFixture(t)
		require.NoError(t, os.Remove(filepath.Join(root, "identities", "seed.yaml")))
		detail, status := identityDirStatus(s)
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "no identity files")
	})

	t.Run("layered empty repo dir falls back to global", func(t *testing.T) {
		repoRoot := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "identities"), 0o700))
		_, status := identityDirStatus(layered(repoRoot, populated(t), false))
		assert.Equal(t, "PASS", status)
	})

	t.Run("repo-only empty repo dir fails", func(t *testing.T) {
		repoRoot := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "identities"), 0o700))
		detail, status := identityDirStatus(layered(repoRoot, populated(t), true))
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "no identity files")
	})

	t.Run("unreadable repo dir fails", func(t *testing.T) {
		if !notRoot {
			t.Skip("permission bits do not bind root or windows")
		}
		s, _, root := newFixture(t)
		dir := filepath.Join(root, "identities")
		require.NoError(t, os.Chmod(dir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		detail, status := identityDirStatus(s)
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "permission denied")
	})

	t.Run("global stat error is not blamed on the repo", func(t *testing.T) {
		if !notRoot {
			t.Skip("permission bits do not bind root or windows")
		}
		globalRoot := populated(t)
		require.NoError(t, os.Chmod(globalRoot, 0o000))
		t.Cleanup(func() { _ = os.Chmod(globalRoot, 0o700) })
		detail, status := identityDirStatus(layered(t.TempDir(), globalRoot, false))
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "error: global identities dir")
		assert.NotContains(t, detail, "not found")
	})

	t.Run("global dir that is a file fails naming the global dir", func(t *testing.T) {
		globalRoot := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(globalRoot, "identities"), []byte("x"), 0o600))
		detail, status := identityDirStatus(layered(t.TempDir(), globalRoot, false))
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "error: global identities dir")
	})
}

func TestIdentityDirStatus_Bundle(t *testing.T) {
	bundleWith := func(t *testing.T, files ...string) string {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
		for _, f := range files {
			require.NoError(t, os.WriteFile(filepath.Join(root, "identities", f), []byte("handle: b\n"), 0o600))
		}
		return root
	}
	store := func(repoRoot, bundleRoot string, repoOnly bool) identity.IdentityStore {
		return identity.NewLayeredStoreWithBundle(identity.NewStore(repoRoot),
			identity.NewStore(bundleRoot), identity.NewStore(t.TempDir()), repoOnly)
	}
	emptyRepo := func(t *testing.T) string {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
		return root
	}

	t.Run("repo-only, bundle holds identities, empty repo dir passes", func(t *testing.T) {
		b := bundleWith(t, "b.yaml")
		res := identityDirResult(store(emptyRepo(t), b, true), nil)
		assert.Equal(t, "PASS", res.Status, res.Detail)
		assert.Contains(t, res.Detail, identity.NewStore(b).IdentitiesDir())
	})

	t.Run("layered, bundle only, no repo dir, no global passes", func(t *testing.T) {
		b := bundleWith(t, "b.yaml")
		res := identityDirResult(store(t.TempDir(), b, false), nil)
		assert.Equal(t, "PASS", res.Status, res.Detail)
	})

	t.Run("broken bundle dir fails", func(t *testing.T) {
		b := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(b, "identities"), []byte("x"), 0o600))
		for _, repoOnly := range []bool{true, false} {
			res := identityDirResult(store(emptyRepo(t), b, repoOnly), nil)
			assert.Equal(t, "FAIL", res.Status)
			assert.Contains(t, res.Detail, "bundle identities dir")
		}
	})

	t.Run("empty bundle dir does not mask a repo-only failure", func(t *testing.T) {
		res := identityDirResult(store(emptyRepo(t), bundleWith(t), true), nil)
		assert.Equal(t, "FAIL", res.Status)
	})
}

func TestClassifyOrphans_IdentityPresence(t *testing.T) {
	marked := func(h string) []orphanFile { return []orphanFile{{handle: h, marked: true}} }

	t.Run("stat error is reported, not classified", func(t *testing.T) {
		if os.Geteuid() == 0 || runtime.GOOS == "windows" {
			t.Skip("permission bits do not bind root or windows")
		}
		root := t.TempDir()
		dir := filepath.Join(root, "identities")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.Chmod(dir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		status, detail := classifyOrphans(marked("x"), identity.NewStore(root))
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "cannot tell whether an identity exists: x")
		assert.NotContains(t, detail, "safe to delete")
	})

	t.Run("repo-only global-only identity is unvendored, not removed", func(t *testing.T) {
		repoRoot, globalRoot := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o700))
		writeIdentity(t, globalRoot, "gonly", "name: G\nhandle: gonly\nkind: agent\n")
		ls := identity.NewLayeredStoreWithBundle(identity.NewStore(repoRoot), nil, identity.NewStore(globalRoot), true)
		status, detail := classifyOrphans(marked("gonly"), ls)
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "not vendored (exists in the global store) — run ethos vendor: gonly")
		assert.NotContains(t, detail, "no longer exists")

		// A truly absent identity is still "removed".
		_, detail = classifyOrphans(marked("nowhere"), ls)
		assert.Contains(t, detail, "no longer exists")
	})

	t.Run("layered global identity is stale", func(t *testing.T) {
		repoRoot, globalRoot := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o700))
		writeIdentity(t, globalRoot, "gonly", "name: G\nhandle: gonly\nkind: agent\n")
		ls := identity.NewLayeredStore(identity.NewStore(repoRoot), identity.NewStore(globalRoot))
		_, detail := classifyOrphans(marked("gonly"), ls)
		assert.Contains(t, detail, "stale")
	})
}

func TestCheckHumanIdentity(t *testing.T) {
	// Pin USER so resolve.Resolve never falls back to the developer's
	// real shell identity. Each sub-test overrides as needed.
	t.Setenv("USER", "nobody-doctor-test")
	// Set HOME so git config picks up nothing surprising.
	t.Setenv("HOME", t.TempDir())

	t.Run("happy path", func(t *testing.T) {
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\n")
		t.Setenv("USER", "mal")
		r := CheckHumanIdentity(s, ss, "")
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "mal")
	})

	t.Run("fresh install — no identities", func(t *testing.T) {
		// An empty store is the expected first-run state, not a fault: WARN
		// and point the user at `ethos setup`.
		s, ss, root := newFixture(t)
		require.NoError(t, os.Remove(filepath.Join(root, "identities", "seed.yaml")))
		t.Setenv("USER", "ghost")
		r := CheckHumanIdentity(s, ss, "")
		assert.Equal(t, "WARN", r.Status)
		assert.True(t, r.Passed(), "fresh install must not gate doctor's exit")
		assert.Contains(t, r.Detail, "ethos setup")
	})

	t.Run("no match — identities exist but none match", func(t *testing.T) {
		// A real misconfiguration: identities are present but none match the
		// caller. This still FAILs loudly, and the detail is the resolver's
		// own error rather than a prefix over it.
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\n")
		t.Setenv("USER", "ghost")
		r := CheckHumanIdentity(s, ss, "")
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "no identity matches")
	})

	t.Run("malformed file", func(t *testing.T) {
		// A file that matches $USER by name but is malformed YAML is skipped
		// during resolution, so lookup fails with no match. A broken file is
		// a misconfiguration, not a fresh install, so it FAILs.
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "bad", "not: [valid: yaml")
		t.Setenv("USER", "bad")
		r := CheckHumanIdentity(s, ss, "")
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "no identity matches")
	})

	t.Run("ambiguous — two identities share an email", func(t *testing.T) {
		// The collision ethos-u4kq made loud. The check must report what
		// the resolver said: there are TWO matches, not none. The old
		// "no match — " prefix contradicted its own detail.
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\nemail: crew@serenity.ship\n")
		writeIdentity(t, root, "zoe",
			"name: Zoe\nhandle: zoe\nkind: human\nemail: crew@serenity.ship\n")
		t.Setenv("USER", "ghost")
		setGitEmail(t, "crew@serenity.ship")
		r := CheckHumanIdentity(s, ss, "")
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "ambiguous identity: 2 matches")
		assert.Contains(t, r.Detail, "mal, zoe")
		assert.NotContains(t, r.Detail, "no match",
			"the detail must not claim there were no matches when there were two")
	})
}

// setGitEmail points git config at a temp file declaring only user.email,
// so resolution reaches the email step deterministically.
func setGitEmail(t *testing.T, email string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".gitconfig")
	require.NoError(t, os.WriteFile(path, []byte("[user]\n\temail = "+email+"\n"), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func TestCheckDefaultAgent(t *testing.T) {
	// CheckDefaultAgent takes the roots explicitly now: repoRoot guards
	// "in a repo", storeRoot resolves the agent: key. In these single-tree
	// cases both are the fixture dir.
	writeAgentConfig := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".punt-labs"), 0o700))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, ".punt-labs", "ethos.yaml"), []byte(body), 0o600))
		return dir
	}

	t.Run("not in a repo", func(t *testing.T) {
		detail, ok := CheckDefaultAgent("", "")
		assert.True(t, ok)
		assert.Equal(t, "not in a git repo", detail)
	})

	t.Run("repo without config", func(t *testing.T) {
		dir := t.TempDir()
		detail, ok := CheckDefaultAgent(dir, dir)
		assert.True(t, ok)
		assert.Equal(t, "not configured", detail)
	})

	t.Run("repo with agent configured", func(t *testing.T) {
		dir := writeAgentConfig(t, "agent: claude\n")
		detail, ok := CheckDefaultAgent(dir, dir)
		assert.True(t, ok)
		assert.Equal(t, "claude", detail)
	})

	t.Run("malformed config", func(t *testing.T) {
		dir := writeAgentConfig(t, "agent: [not a string")
		detail, ok := CheckDefaultAgent(dir, dir)
		assert.False(t, ok)
		assert.NotEmpty(t, detail)
	})

	// The #370 split: repoRoot guards "in a repo", but the agent resolves
	// from storeRoot. A worktree whose ethos.yaml names a different agent
	// than the store must report the STORE's agent — the one SessionStart
	// and dispatch actually use.
	t.Run("resolves agent from store root not checkout", func(t *testing.T) {
		checkoutRoot := writeAgentConfig(t, "agent: mdm\n")
		storeRoot := writeAgentConfig(t, "agent: bwk\n")

		detail, ok := CheckDefaultAgent(checkoutRoot, storeRoot)
		assert.True(t, ok)
		assert.Equal(t, "bwk", detail,
			"must report the store's agent (what SessionStart/dispatch use)")

		// Regression guard: resolving from the checkout reports the wrong
		// agent — the bug this split fixes.
		buggy, _ := CheckDefaultAgent(checkoutRoot, checkoutRoot)
		assert.Equal(t, "mdm", buggy)
	})
}

func TestCheckDuplicateFields(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\nemail: mal@example.com\ngithub: mal\n")
		writeIdentity(t, root, "zoe",
			"name: Zoe\nhandle: zoe\nkind: human\nemail: zoe@example.com\ngithub: zoe\n")
		detail, ok := CheckDuplicateFields(s, ss)
		assert.True(t, ok, "detail: %s", detail)
		assert.Equal(t, "no duplicates", detail)
	})

	t.Run("duplicate github", func(t *testing.T) {
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\ngithub: shared\n")
		writeIdentity(t, root, "zoe",
			"name: Zoe\nhandle: zoe\nkind: human\ngithub: shared\n")
		detail, ok := CheckDuplicateFields(s, ss)
		assert.False(t, ok)
		assert.Contains(t, detail, "github")
		assert.Contains(t, detail, "shared")
	})

	t.Run("duplicate email", func(t *testing.T) {
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\nemail: same@example.com\n")
		writeIdentity(t, root, "zoe",
			"name: Zoe\nhandle: zoe\nkind: human\nemail: same@example.com\n")
		detail, ok := CheckDuplicateFields(s, ss)
		assert.False(t, ok)
		assert.Contains(t, detail, "email")
		assert.Contains(t, detail, "same@example.com")
	})

	t.Run("ignores empty fields", func(t *testing.T) {
		// Two identities with no email/github must not count as duplicates.
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal", "name: Mal\nhandle: mal\nkind: human\n")
		writeIdentity(t, root, "zoe", "name: Zoe\nhandle: zoe\nkind: human\n")
		detail, ok := CheckDuplicateFields(s, ss)
		assert.True(t, ok, "detail: %s", detail)
		assert.Equal(t, "no duplicates", detail)
	})
}

// simulateWindows points BOTH platform seams at Windows for the duration of
// t: sandboxGOOS (does FileMode carry execute bits?) and sandboxSupported
// (can the execution sandbox run at all?). They are separate facts about
// different platform sets — see sandboxGOOS's doc comment — but on Windows
// both hold, so a test that moves only one models a platform that does not
// exist. Setting them through one helper is what keeps that impossible.
func simulateWindows(t *testing.T) {
	t.Helper()
	origGOOS, origSupported := sandboxGOOS, sandboxSupported
	sandboxGOOS, sandboxSupported = "windows", false
	t.Cleanup(func() { sandboxGOOS, sandboxSupported = origGOOS, origSupported })
}

func TestCheckSealHook(t *testing.T) {
	// mark writes the enabled marker so a repo reads as "enabled here".
	mark := func(t *testing.T, dir string) {
		t.Helper()
		zone := filepath.Join(dir, ".punt-labs", "ethos")
		require.NoError(t, os.MkdirAll(zone, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(zone, "enabled"), nil, 0o644))
	}
	// writeEnabledHook creates an enabled repo whose pre-commit holds body.
	writeEnabledHook := func(t *testing.T, body string) string {
		t.Helper()
		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o755))
		mark(t, dir)
		return dir
	}

	t.Run("not in a repo", func(t *testing.T) {
		r := CheckSealHook("")
		assert.True(t, r.Passed())
		assert.Equal(t, "not in a repo", r.Detail)
	})

	// --- Four-state model (§2.7/§2.11) ---

	t.Run("dormant: no marker, no hook → PASS not enabled here", func(t *testing.T) {
		dir := repoTemp(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	t.Run("dormant: no marker, foreign hook without ethos → PASS not enabled here", func(t *testing.T) {
		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"), 0o755))
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	// M1: a diagnostic read must not execute untrusted third-party shell to
	// reach a verdict that does not depend on the execution's outcome. In a
	// dormant repo (no enabled marker) the presence check answers from
	// hasMarkerSection's lexical scan alone; hookInvocationObserved must
	// never run.
	t.Run("dormant: no marker, foreign hook is never executed (M1)", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		witness := filepath.Join(dir, "witness")
		body := "#!/bin/sh\ntouch " + shQuote(witness) + "\ntrue\n"
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o755))

		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
		_, err := os.Stat(witness)
		assert.True(t, os.IsNotExist(err),
			"the foreign hook must never run when ethos is not enabled here — witness file was created")
	})

	// S2/P1: DES-077 documents the dormant state as an unconditional PASS —
	// "a never-enabled or disabled repo must not fail." Pre-fix, the
	// sandbox call ran before the marker gate, so ANY sandbox-infrastructure
	// failure unrelated to the hook at all — no git on PATH, here — leaked
	// through as a FAIL on a repo that was never enabled, contradicting
	// that documented state. No `exec.LookPath("git")` skip: the whole
	// point is that a dormant repo never FAILs, even when the sandbox
	// couldn't have run at all; it is a WARN, since the hooks git really runs
	// cannot be inspected.
	t.Run("dormant: no git on PATH is WARN cannot verify hooks — execution never attempted (S2/P1)", func(t *testing.T) {
		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\ntrue\n"), 0o755))

		t.Setenv("PATH", t.TempDir()) // no git anywhere on PATH
		r := CheckSealHook(dir)
		// The degraded hooks-dir lookup is named, and it never fails a dormant
		// repo.
		assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
		assert.True(t, strings.HasPrefix(r.Detail, "cannot verify hooks:"), r.Detail)
		assert.Contains(t, r.Detail, "install git")
	})

	// P5 superseded by qodo #5 (PR #515 review): P5 originally pinned PASS
	// here because before M1, only EXECUTION (hookInvocationObserved) could
	// see a standalone, unmarked call, and djb's review explicitly rejected
	// executing every foreign hook in every dormant repo just to tell PASS
	// from WARN. That reasoning was never about lexical detection — a
	// literal, textually-visible `ethos hook pre-commit` needs no execution to
	// see. The dormant branch now also runs looksLikeInvocation (a pure
	// regex scan, same as the "gated-but-unenabled" marked-section case
	// already used), so this same fixture now WARNs. What P5's trade-off
	// still protects — and TestCheckSealHook's eval case right below pins
	// — is a call assembled dynamically (eval, a variable) that no lexical
	// scan, marker-based or pattern-based, can see without executing it.
	t.Run("dormant: a textually-literal unmarked standalone call now WARNs (qodo #5, supersedes P5)", func(t *testing.T) {
		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		// No BEGIN/END markers — hasMarkerSection cannot see this call —
		// but it is unconditional and textually literal: a real commit in
		// this real (dormant) repo would actually invoke ethos, and the
		// call is visible to a lexical scan without running anything.
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos hook pre-commit || exit 2\n"), 0o755))

		r := CheckSealHook(dir)
		assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "chained but ethos not enabled here")
	})

	// The narrower residual P5 leaves standing: a call built at runtime
	// (eval, a variable) is invisible to looksLikeInvocation exactly the
	// way it is invisible to hasMarkerSection — no lexical scan, only
	// execution, can see through it, and the dormant branch still does not
	// execute. This is the same djb-reviewed trade-off P5 named, narrowed
	// to the one shape lexical detection genuinely cannot close.
	t.Run("dormant: an eval-obscured standalone call still PASSes — the narrower residual lexical scanning cannot close", func(t *testing.T) {
		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\ncmd='ethos hook pre-commit'\neval \"$cmd\"\n"), 0o755))

		r := CheckSealHook(dir)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	t.Run("heredoc-quoted marker on a never-enabled repo → PASS not WARN", func(t *testing.T) {
		// A foreign hook that only documents the marker text inside a heredoc,
		// on a repo with no enabled marker, must not read as a chained section
		// (no false WARN) — the marker scan consults the heredoc mask (E2).
		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		body := "#!/bin/sh\ncat <<'EOF'\n# --- BEGIN ETHOS DES-058 SEAL ---\nEOF\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o755))
		r := CheckSealHook(dir)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	t.Run("gated-but-unenabled: chained hook, no marker → WARN", func(t *testing.T) {
		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"ethos hook pre-commit || exit 2\n# --- END ETHOS DES-058 SEAL ---\n"
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o755))
		r := CheckSealHook(dir)
		assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "not enabled here")
	})

	t.Run("enabled but no pre-commit hook → FAIL", func(t *testing.T) {
		dir := repoTemp(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
		mark(t, dir)
		r := CheckSealHook(dir)
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "no pre-commit hook")
		assert.Contains(t, r.Detail, "ethos enable")
	})

	t.Run("marker stat error is not read as disabled → FAIL", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod 0o000 does not deny directory search on Windows; this failure mode is Unix-specific")
		}
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permissions")
		}
		dir := repoTemp(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
		zone := filepath.Join(dir, ".punt-labs", "ethos")
		require.NoError(t, os.MkdirAll(zone, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(zone, "enabled"), nil, 0o644))
		// Strip permissions on the zone so stat of the marker fails with a
		// permission error rather than IsNotExist.
		require.NoError(t, os.Chmod(zone, 0o000))
		t.Cleanup(func() { _ = os.Chmod(zone, 0o755) })
		r := CheckSealHook(dir)
		assert.Equal(t, "FAIL", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "cannot determine enablement")
	})

	// M4: on a platform this sandbox cannot exercise, an enabled repo with a
	// perfectly healthy chained hook must not read as FAIL "not chained" —
	// that would be a silently wrong answer in the dangerous direction on
	// every Windows install. WARN, honestly, instead.
	t.Run("unsupported platform → WARN cannot verify, not a false FAIL (M4)", func(t *testing.T) {
		simulateWindows(t)

		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"ethos hook pre-commit || exit 2\n# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
		assert.True(t, r.Passed(), "an unverifiable platform must not gate doctor's exit status")
		assert.Contains(t, r.Detail, "cannot verify")
		assert.NotContains(t, r.Detail, "not chained")
	})

	// qodo #12 (PR #515): before execution-based verification, Windows
	// still got SOME lexical check, which could catch a hook with zero
	// textual trace of the required call. M4's blanket WARN regressed that
	// specific case — one that would fail even the OLD check — to "looks
	// fine." A hook with no textual evidence at all must still FAIL on
	// this platform; only a hook WITH textual evidence (M4's actual case)
	// gets the honest "cannot verify" WARN.
	t.Run("unsupported platform, no textual evidence of the call at all → FAIL, not a false WARN (qodo #12)", func(t *testing.T) {
		simulateWindows(t)

		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"echo nothing to see here\n# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed(), "detail: %s", r.Detail)
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "no textual evidence")
	})

	// --- Enabled: seal-call detection (marker present) ---

	t.Run("standalone seal hook", func(t *testing.T) {
		dir := writeEnabledHook(t, "#!/bin/sh\n# DES-058\nethos hook pre-commit || exit 2\n")
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "standalone")
	})

	t.Run("chained seal section", func(t *testing.T) {
		body := "#!/bin/sh\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n" +
			"# --- BEGIN ETHOS DES-058 SEAL ---\nethos hook pre-commit || exit 2\n" +
			"# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "chained")
	})

	t.Run("stale section without seal call", func(t *testing.T) {
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"echo placeholder\n# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "stale")
	})

	t.Run("commented-out seal call is not active", func(t *testing.T) {
		// A disabled invocation must not read as active — that is exactly
		// the silent-absence recurrence the check exists to catch.
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"# if ! ethos hook pre-commit; then exit 2; fi\n" +
			"# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "stale")
	})

	t.Run("mention in a foreign comment is not active", func(t *testing.T) {
		body := "#!/bin/sh\n# TODO: wire up ethos hook pre-commit here\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not chained")
	})

	t.Run("inline trailing comment mention is not active", func(t *testing.T) {
		// The phrase in an inline comment after code must not read as a call.
		body := "#!/bin/sh\necho ok # ethos hook pre-commit\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not chained")
	})

	t.Run("phrase as arguments to another command is not active", func(t *testing.T) {
		// `ethos hook pre-commit` passed as args to echo is not a call (C1).
		body := "#!/bin/sh\necho ethos hook pre-commit\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not chained")
	})

	t.Run("comment after a word-break char is not active", func(t *testing.T) {
		// Shell starts a comment after ';' or '&', not just whitespace (C2).
		for _, body := range []string{
			"#!/bin/sh\ntrue;# ethos hook pre-commit\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n",
			"#!/bin/sh\ntrue &# ethos hook pre-commit\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n",
		} {
			dir := writeEnabledHook(t, body)
			r := CheckSealHook(dir)
			assert.False(t, r.Passed(), "body: %q", body)
			assert.Contains(t, r.Detail, "not chained")
		}
	})

	t.Run("call after a separator is active", func(t *testing.T) {
		// A genuine command-position call (after '&&') must still PASS. The
		// left side must actually succeed (`true`, not an undefined
		// `precheck`) — CheckSealHook now proves activity by executing the
		// hook (ethos-kcbv), and a real shell honors `&&` short-circuiting:
		// a failing left side means the right side never runs.
		body := "#!/bin/sh\ntrue && ethos hook pre-commit\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
	})

	t.Run("string-literal mention is not an active call", func(t *testing.T) {
		// echo/printf text containing the phrase must not read as a call.
		dir := writeEnabledHook(t, "#!/bin/sh\necho \"remember to run audit seal\"\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n")
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not chained")
	})

	// H3: the sandbox is an empty, freshly `git init`'d temp repo — no
	// commits, nothing staged. A host section guarding on that state (an
	// ordinary "only run if files are staged" check, which is healthy in a
	// real commit) short-circuits inside the sandbox for reasons that exist
	// only there, and the chained ethos section never runs. Pre-H1 this
	// collapsed to the same "stale — run `ethos enable`" FAIL as a genuinely
	// disabled seal call, which is the wrong remedy: re-running `ethos
	// enable` fixes nothing here. The message must name what actually
	// happened instead.
	t.Run("H3: a host section that exits before the chained ethos call gets a specific message, not a bare stale", func(t *testing.T) {
		body := "#!/bin/sh\n[ -f /nonexistent-marker-ethos-doctor-h3-probe ] || exit 1\n" +
			"# --- BEGIN ETHOS DES-058 SEAL ---\nethos hook pre-commit || exit 2\n# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "exited 1 before reaching the ethos call")
		assert.NotContains(t, r.Detail, "stale — run",
			"a host-section failure needs its own message, not the misleading 'stale, run ethos enable' remedy, which fixes nothing here")
	})

	// P4: when the installed marker section is BYTE-IDENTICAL to what this
	// ethos build would install today, a host-section-exited failure must
	// WARN, not FAIL "stale" — the ethos section is demonstrably not the
	// problem, and `ethos enable` re-chaining identical content would
	// reproduce this exact same failure, making that remedy useless.
	// Unlike the H3 test above (a hand-written section that does not match
	// what Chain would produce, so it correctly stays FAIL), this uses the
	// real githook.Chain output.
	t.Run("P4: a genuinely current section still WARNs, not FAILs, when the host section exits first", func(t *testing.T) {
		dir := repoTemp(t)
		hooksDir := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooksDir, 0o755))
		hookPath := filepath.Join(hooksDir, "pre-commit")
		guard := "#!/bin/sh\n[ -f /nonexistent-marker-ethos-doctor-p4-probe ] || exit 1\n"
		require.NoError(t, os.WriteFile(hookPath, []byte(guard), 0o755))
		_, err := githook.Chain(hookPath, sealHookSpec.Canonical, sealHookSpec.Tag, sealHookSpec.Ident)
		require.NoError(t, err)
		mark(t, dir)

		r := CheckSealHook(dir)
		assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "exited (status 1) before reaching the ethos call")
		assert.Contains(t, r.Detail, "not stale")
		assert.Contains(t, r.Detail, "host-section problem")
	})

	t.Run("printf note inside a section is stale, not active", func(t *testing.T) {
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"printf 'audit seal is disabled\\n'\n# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "stale")
	})

	t.Run("seal call in a non-shell hook fails", func(t *testing.T) {
		// A shell seal call pasted into a Python hook can never run as sh.
		dir := writeEnabledHook(t, "#!/usr/bin/env python3\nethos hook pre-commit\n")
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not a shell")
	})

	// The executable-bit check has two arms, pinned explicitly here rather
	// than inherited from the test host, so both are exercised wherever the
	// suite runs. Unix: the bit means what it says, so a hook without it
	// FAILs with the chmod remedy. Windows: os.Stat derives permission bits
	// from the read-only attribute alone, so no regular file ever carries
	// 0o111 and the check must not run at all.
	t.Run("non-executable hook fails with chmod remedy", func(t *testing.T) {
		orig := sandboxGOOS
		sandboxGOOS = "linux"
		t.Cleanup(func() { sandboxGOOS = orig })

		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos hook pre-commit || exit 2\n"), 0o644))
		mark(t, dir)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not executable")
		assert.Contains(t, r.Detail, "chmod +x")
	})

	// qodo (PR #515): the sandbox writes its own copy of body at 0o755
	// regardless of the installed file's own mode, so a non-executable hook
	// — one git would never run — was still executed inside the sandbox to
	// diagnose that same non-executable state. A witness file proves
	// whether the body actually ran.
	t.Run("non-executable hook is never executed by the sandbox", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		orig := sandboxGOOS
		sandboxGOOS = "linux"
		t.Cleanup(func() { sandboxGOOS = orig })

		dir := repoTemp(t)
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		witness := filepath.Join(t.TempDir(), "witness")
		body := "#!/bin/sh\ntouch " + shQuote(witness) + "\nethos hook pre-commit || exit 2\n"
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o644))
		mark(t, dir)

		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not executable")

		_, statErr := os.Stat(witness)
		assert.True(t, os.IsNotExist(statErr),
			"a non-executable hook must never run inside the sandbox — git would never run it either")
	})

	// Bugbot (PR #515): moving the executable-bit check ahead of the sandbox
	// call fixed qodo #3 on Unix but made it run on Windows too, where
	// os.Stat derives a regular file's permission bits from the read-only
	// attribute alone — 0o444 or 0o666, never 0o111. Every hook on Windows
	// therefore looked non-executable: every enabled install FAILed with an
	// unactionable `chmod +x`, and the qodo #12 FAIL/WARN split below it
	// became unreachable. Both cases of that split are asserted here, so the
	// test proves the branch is reachable again rather than only proving the
	// wrong FAIL is gone.
	t.Run("Windows: the executable-bit check does not run and the platform split is reachable", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			section    string
			wantStatus string
			wantDetail string
		}{
			{
				name:       "textual evidence of the call → WARN cannot verify",
				section:    "ethos hook pre-commit || exit 2\n",
				wantStatus: "WARN",
				wantDetail: "cannot verify",
			},
			{
				name:       "no textual evidence of the call → FAIL",
				section:    "echo nothing to see here\n",
				wantStatus: "FAIL",
				wantDetail: "no textual evidence",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				simulateWindows(t)

				dir := repoTemp(t)
				hooks := filepath.Join(dir, ".git", "hooks")
				require.NoError(t, os.MkdirAll(hooks, 0o755))
				body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
					tc.section + "# --- END ETHOS DES-058 SEAL ---\n"
				// 0o644 — no execute bits, the mode every regular file
				// reports on Windows.
				require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o644))
				mark(t, dir)

				r := CheckSealHook(dir)
				assert.Equal(t, tc.wantStatus, r.Status, "detail: %s", r.Detail)
				assert.Contains(t, r.Detail, tc.wantDetail)
				assert.NotContains(t, r.Detail, "not executable",
					"the executable bit has no execute semantics on Windows — this check must not run there")
				assert.NotContains(t, r.Detail, "chmod +x",
					"chmod is not a remedy an operator can apply on Windows")
			})
		}
	})

	t.Run("enabled foreign hook without seal → FAIL not chained", func(t *testing.T) {
		dir := writeEnabledHook(t, "#!/bin/sh\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n")
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not chained")
	})

	t.Run("multi-line arithmetic above the seal section still PASSes", func(t *testing.T) {
		// A healthy repo whose hook has multi-line arithmetic above the real
		// seal section must not FAIL "stale": the arithmetic's second line
		// must not be misread as opening a heredoc that masks the seal call.
		body := "#!/bin/sh\nx=$((1 +\n2 << 3))\n" +
			"# --- BEGIN ETHOS DES-058 SEAL ---\nethos hook pre-commit || exit 2\n# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "chained")
	})

	t.Run("heredoc-quoted seal mention is not an active call", func(t *testing.T) {
		// A hook that only documents the seal in a heredoc body (usage/help
		// text) never runs it — CheckSealHook must NOT return PASS, or the
		// silent-absence bug this branch exists to close reopens.
		body := "#!/bin/sh\ncat <<'EOF'\nethos hook pre-commit\nEOF\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "not chained")
	})

	t.Run("gitdir file redirects hooks path", func(t *testing.T) {
		// A .git file points directly at a dir with hooks/ and no commondir
		// (git init --separate-git-dir) — the hooks dir is target/hooks.
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		real := filepath.Join(t.TempDir(), "gitdir")
		wt := t.TempDir()
		cmd := testhelpers.GitCommand("init", "-q", "--separate-git-dir", real, wt)
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		hooks := filepath.Join(real, "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos hook pre-commit || exit 2\n"), 0o755))
		mark(t, wt)
		r := CheckSealHook(wt)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
	})

	t.Run("hand-built worktree layout is not trusted without git", func(t *testing.T) {
		// Real worktree layout: the .git file points at
		// <main>/.git/worktrees/<name>, which has a commondir file (../..)
		// back to the main .git — where the hooks actually live. The seal
		// hook at the per-worktree admin dir must be ignored; only the
		// common hooks dir counts.
		mainGit := t.TempDir() // stands in for <main>/.git
		commonHooks := filepath.Join(mainGit, "hooks")
		require.NoError(t, os.MkdirAll(commonHooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(commonHooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos hook pre-commit || exit 2\n"), 0o755))

		wtAdmin := filepath.Join(mainGit, "worktrees", "wt")
		require.NoError(t, os.MkdirAll(filepath.Join(wtAdmin, "hooks"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(wtAdmin, "commondir"),
			[]byte("../..\n"), 0o644))
		// A decoy hook at the dead path git never runs — must be ignored.
		require.NoError(t, os.WriteFile(filepath.Join(wtAdmin, "hooks", "pre-commit"),
			[]byte("#!/bin/sh\necho decoy\n"), 0o755))

		wt := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"),
			[]byte("gitdir: "+wtAdmin+"\n"), 0o644))
		mark(t, wt)

		// git does not know this layout (the main .git is not a repository), so
		// the commondir guess is reported, not judged: an enabled repo FAILs.
		r := CheckSealHook(wt)
		assert.Equal(t, "FAIL", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "cannot verify hooks:")
	})

	t.Run("worktree via real git worktree add", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		mainDir := t.TempDir()
		gitRun := func(dir string, args ...string) {
			t.Helper()
			cmd := testhelpers.GitCommand(append([]string{"-c", "commit.gpgsign=false"}, args...)...)
			cmd.Dir = dir
			cmd.Env = append(cmd.Env,
				"HOME="+t.TempDir(),
				"GIT_CONFIG_GLOBAL=/dev/null",
				"GIT_CONFIG_SYSTEM=/dev/null",
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
		gitRun(mainDir, "init", "-q")
		gitRun(mainDir, "commit", "--allow-empty", "-q", "-m", "init")

		// The hook git actually runs lives in the main repo's hooks dir.
		hooks := filepath.Join(mainDir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos hook pre-commit || exit 2\n"), 0o755))

		wtDir := filepath.Join(t.TempDir(), "wt")
		gitRun(mainDir, "worktree", "add", "-q", wtDir, "-b", "b")
		mark(t, wtDir)

		r := CheckSealHook(wtDir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
	})

	t.Run("honors core.hooksPath like the installer", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		repo := t.TempDir()
		cmd := testhelpers.GitCommand("-C", repo, "init", "-q")
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git init: %s", out)
		cmd = testhelpers.GitCommand("-C", repo, "config", "core.hooksPath", ".husky")
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "git config: %s", out)

		// The seal lives where git runs hooks — the tracked .husky dir, the
		// same path the installer resolves via `git rev-parse --git-path hooks`.
		husky := filepath.Join(repo, ".husky")
		require.NoError(t, os.MkdirAll(husky, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(husky, "pre-commit"),
			[]byte("#!/bin/sh\nethos hook pre-commit || exit 2\n"), 0o755))
		// A seal in .git/hooks must NOT satisfy the check — git never runs it.
		defaultHooks := filepath.Join(repo, ".git", "hooks")
		require.NoError(t, os.MkdirAll(defaultHooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(defaultHooks, "pre-commit"),
			[]byte("#!/bin/sh\necho decoy\n"), 0o755))
		mark(t, repo)

		r := CheckSealHook(repo)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
	})

	// LOW: a diverted core.hooksPath is exactly the kind of surprise an
	// operator running `ethos doctor` needs named — doctor is the one
	// surface that already inspects hook state closely enough to say it.
	// githook.HooksDir's second return value carried this warning all
	// along; checkHookPresence used to discard it with `dir, _ :=`.
	t.Run("diverted core.hooksPath inside the work tree is named in Detail (LOW)", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		repo := t.TempDir()
		cmd := testhelpers.GitCommand("-C", repo, "init", "-q")
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git init: %s", out)
		cmd = testhelpers.GitCommand("-C", repo, "config", "core.hooksPath", ".husky")
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "git config: %s", out)

		husky := filepath.Join(repo, ".husky")
		require.NoError(t, os.MkdirAll(husky, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(husky, "pre-commit"),
			[]byte("#!/bin/sh\nethos hook pre-commit || exit 2\n"), 0o755))
		mark(t, repo)

		r := CheckSealHook(repo)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "core.hooksPath places hooks at")
		assert.Contains(t, r.Detail, "inside the work tree")
	})
}

// TestCheckTrailerHook covers the trailer-specific shape of checkHookPresence
// (ethos-bfml/ethos-hy40): the commit-msg file name, the "hook
// commit-trailers" invocation, and NeedsMsgArg's dependency on a scratch
// message file. checkHookPresence's shared control flow (dormant/WARN/FAIL
// states, comment/heredoc/eval handling) is already exercised exhaustively
// by TestCheckSealHook against the same function; this suite does not repeat
// that ground.
func TestCheckTrailerHook(t *testing.T) {
	mark := func(t *testing.T, dir string) {
		t.Helper()
		zone := filepath.Join(dir, ".punt-labs", "ethos")
		require.NoError(t, os.MkdirAll(zone, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(zone, "enabled"), nil, 0o644))
	}
	writeEnabledHook := func(t *testing.T, body string) string {
		t.Helper()
		dir := repoTemp(t)
		hooksDir := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooksDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "commit-msg"), []byte(body), 0o755))
		mark(t, dir)
		return dir
	}

	t.Run("not in a repo", func(t *testing.T) {
		r := CheckTrailerHook("")
		assert.True(t, r.Passed())
		assert.Equal(t, "not in a repo", r.Detail)
	})

	t.Run("dormant: no marker, no hook → PASS not enabled here", func(t *testing.T) {
		dir := repoTemp(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
		r := CheckTrailerHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	t.Run("enabled but no commit-msg hook → FAIL", func(t *testing.T) {
		dir := repoTemp(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
		mark(t, dir)
		r := CheckTrailerHook(dir)
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "no commit-msg hook")
		assert.Contains(t, r.Detail, "ethos enable")
	})

	t.Run("enabled, real call needing $1 — active without a fixed message file arg baked in", func(t *testing.T) {
		// The commit-msg hook itself refuses to call ethos without $1; the
		// sandbox in hookInvocationObserved supplies that argument via
		// spec.NeedsMsgArg, not the test fixture.
		body := "#!/bin/sh\n[ -z \"$1\" ] && exit 0\nethos hook commit-msg\n"
		dir := writeEnabledHook(t, body)
		r := CheckTrailerHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "standalone")
	})

	t.Run("chained trailer section active", func(t *testing.T) {
		body := "#!/bin/sh\ntrue # stand-in for a foreign host hook's own logic\n" +
			"# --- BEGIN ETHOS DES-054 TRAILER ---\n" +
			"[ -z \"$1\" ] && exit 0\nethos hook commit-msg\n" +
			"# --- END ETHOS DES-054 TRAILER ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckTrailerHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "chained")
	})

	t.Run("stale section without an active call", func(t *testing.T) {
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-054 TRAILER ---\n" +
			"echo placeholder\n# --- END ETHOS DES-054 TRAILER ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckTrailerHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "stale")
	})

	t.Run("gated-but-unenabled: chained hook, no marker → WARN", func(t *testing.T) {
		dir := repoTemp(t)
		hooksDir := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooksDir, 0o755))
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-054 TRAILER ---\n" +
			"[ -z \"$1\" ] && exit 0\nethos hook commit-msg\n" +
			"# --- END ETHOS DES-054 TRAILER ---\n"
		require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "commit-msg"), []byte(body), 0o755))
		r := CheckTrailerHook(dir)
		assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "not enabled here")
	})

	t.Run("the real DES-054 hook (chained via githook.Chain) is PASS", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		dir := repoTemp(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
		_, err := githook.Chain(filepath.Join(dir, ".git", "hooks", "commit-msg"), hooks.CommitMsg, hooks.TrailerTag, hooks.TrailerIdent)
		require.NoError(t, err)
		mark(t, dir)
		r := CheckTrailerHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "chained")
	})
}

// TestCheckHookPresence_Bfml_Hy40Regression pins the actual ethos-bfml/
// ethos-hy40 defect, reproduced against pre-fix source: an ENABLED repo
// whose commit-msg hook has been hand-removed (a host-clobbered install,
// per hy40's filed scenario) reported no FAIL anywhere. RunAll had no
// trailer presence check at all before this change — only
// "Trailer hook currency", which PASSes "no Trailer hook section installed"
// by its own deliberate, documented design regardless of enablement.
//
// Confirmed against the pre-fix tree (commit 1d27319, this branch's base):
// RunAll on exactly this fixture returned zero FAIL results — "Trailer hook
// currency" read PASS "no Trailer hook section installed" and no other
// check named the trailer hook at all. AllPassed was true.
func TestCheckHookPresence_Bfml_Hy40Regression(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	s, ss, root := newFixture(t)
	writeIdentity(t, root, "mal", "name: Mal\nhandle: mal\nkind: human\n")
	t.Setenv("USER", "mal")
	t.Setenv("HOME", t.TempDir())

	repo := repoTemp(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755))
	// The seal hook is installed and healthy — only the trailer side is
	// missing, isolating the exact gap hy40 named.
	_, err := githook.Chain(filepath.Join(repo, ".git", "hooks", "pre-commit"), hooks.PreCommit, hooks.SealTag, hooks.SealIdent)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), nil, 0o644))

	results := RunAll(s, ss, repo, nil, repo, nil, nil)

	var trailerPresence *Result
	for i := range results {
		if results[i].Name == "Audit trailer hook" {
			trailerPresence = &results[i]
		}
	}
	require.NotNil(t, trailerPresence, "results: %+v", results)
	assert.Equal(t, "FAIL", trailerPresence.Status,
		"an enabled repo with no commit-msg hook must FAIL the trailer presence check")
	assert.Contains(t, trailerPresence.Detail, "no commit-msg hook")
	assert.False(t, AllPassed(results),
		"the aggregate must show at least one FAIL — this is exactly the false-all-green bfml/hy40 reported")
}

// currencyTestSpec is a HookSpec fixture independent of the real hooks.*
// constants, so these tests exercise CheckHookCurrency's own logic without
// depending on the shape of the real seal/trailer scripts.
var currencyTestSpec = HookSpec{
	Name:      "Test hook",
	File:      "pre-commit",
	Tag:       "ETHOS TEST HOOK",
	Ident:     "test hook ident fingerprint",
	Canonical: []byte("#!/bin/sh\n# test hook ident fingerprint\necho hello\n"),
}

// currencyRepo creates a bare .git/hooks dir for CheckHookCurrency tests.
func currencyRepo(t *testing.T) string {
	t.Helper()
	dir := repoTemp(t) // a real repository: a hand-made .git is a degraded lookup
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
	return dir
}

func TestCheckHookCurrency(t *testing.T) {
	hookPath := func(dir string) string {
		return filepath.Join(dir, ".git", "hooks", currencyTestSpec.File)
	}

	t.Run("not in a repo", func(t *testing.T) {
		r := CheckHookCurrency("", currencyTestSpec)
		assert.True(t, r.Passed())
		assert.Equal(t, "not in a repo", r.Detail)
	})

	t.Run("no hook file present -> PASS nothing installed", func(t *testing.T) {
		dir := currencyRepo(t)
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Equal(t, "Test hook currency", r.Name)
		assert.Contains(t, r.Detail, "no Test hook section installed")
	})

	t.Run("hook file present, no matching tag -> PASS", func(t *testing.T) {
		dir := currencyRepo(t)
		require.NoError(t, os.WriteFile(hookPath(dir), []byte("#!/bin/sh\nrun_lint || exit 1\n"), 0o755))
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "no Test hook section installed")
	})

	t.Run("matching tag, current -> PASS", func(t *testing.T) {
		dir := currencyRepo(t)
		_, err := githook.Chain(hookPath(dir), currencyTestSpec.Canonical, currencyTestSpec.Tag, currencyTestSpec.Ident)
		require.NoError(t, err)
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "matches this ethos build")
		assert.Contains(t, r.Detail, "sha256:")
	})

	t.Run("matching tag, stale -> WARN with both hash prefixes", func(t *testing.T) {
		dir := currencyRepo(t)
		oldSrc := []byte("#!/bin/sh\n# test hook ident fingerprint\necho an older body\n")
		_, err := githook.Chain(hookPath(dir), oldSrc, currencyTestSpec.Tag, currencyTestSpec.Ident)
		require.NoError(t, err)
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "WARN", r.Status)
		assert.Contains(t, r.Detail, "differs from what this ethos build would install")
		assert.Contains(t, r.Detail, "installed sha256:")
		assert.Contains(t, r.Detail, "current sha256:")
		assert.Contains(t, r.Detail, "ethos enable")
	})

	t.Run("tampered fingerprint -> FAIL", func(t *testing.T) {
		dir := currencyRepo(t)
		body := "#!/bin/sh\n# --- BEGIN " + currencyTestSpec.Tag + " ---\n" +
			"not ours\n# --- END " + currencyTestSpec.Tag + " ---\n"
		require.NoError(t, os.WriteFile(hookPath(dir), []byte(body), 0o755))
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "fingerprint")
	})

	t.Run("duplicate sections -> FAIL", func(t *testing.T) {
		dir := currencyRepo(t)
		one := githook.ExpectedSection(currencyTestSpec.Tag, currencyTestSpec.Canonical)
		body := append(append([]byte("#!/bin/sh\n"), one...), one...)
		require.NoError(t, os.WriteFile(hookPath(dir), body, 0o755))
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "FAIL", r.Status, "detail: %s", r.Detail)
		assert.Equal(t, `multiple "`+currencyTestSpec.Tag+`" sections found`, r.Detail)
	})

	t.Run("truncated section -> FAIL", func(t *testing.T) {
		dir := currencyRepo(t)
		body := "#!/bin/sh\n# --- BEGIN " + currencyTestSpec.Tag + " ---\n# " +
			currencyTestSpec.Ident + "\necho hi\n"
		require.NoError(t, os.WriteFile(hookPath(dir), []byte(body), 0o755))
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "hand-truncated")
	})

	t.Run("unterminated heredoc masks a real section -> FAIL", func(t *testing.T) {
		dir := currencyRepo(t)
		body := "#!/bin/sh\ncat <<EOF\n# --- BEGIN " + currencyTestSpec.Tag + " ---\n# " +
			currencyTestSpec.Ident + "\necho hi\n# --- END " + currencyTestSpec.Tag + " ---\n"
		require.NoError(t, os.WriteFile(hookPath(dir), []byte(body), 0o755))
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "FAIL", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "unterminated here-document")
	})

	// A pre-marker standalone hook (predates PR #357/83e75ec: ident on line 2,
	// no BEGIN markers anywhere) is active and ethos-owned — CheckSealHook
	// already recognizes it as "standalone seal hook active". InstalledSection
	// reports ok=false for it (no BEGIN to find), but that must not collapse
	// to a silent PASS "nothing installed": the hook IS installed, just in the
	// old format, and doctor must WARN rather than hide it.
	t.Run("legacy pre-marker standalone -> WARN, not PASS", func(t *testing.T) {
		dir := currencyRepo(t)
		require.NoError(t, os.WriteFile(hookPath(dir), currencyTestSpec.Canonical, 0o755))
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "legacy")
		assert.Contains(t, r.Detail, "ethos enable")
	})

	t.Run("unreadable hook file -> FAIL with permission remedy", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod 0o000 leaves a file readable on Windows (it only sets the read-only attribute); this failure mode is Unix-specific")
		}
		if os.Geteuid() == 0 {
			t.Skip("root bypasses file permissions")
		}
		dir := currencyRepo(t)
		path := hookPath(dir)
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0o644))
		require.NoError(t, os.Chmod(path, 0o000))
		t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "FAIL", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "cannot read")
		assert.Contains(t, r.Detail, "check file permissions")
	})

	// A non-permission read error (here, the hook path is a directory —
	// os.ErrIsDir) must not carry the permission-specific remedy: "check
	// file permissions" would misdirect an operator debugging EISDIR,
	// ELOOP, or another I/O failure.
	t.Run("non-permission read error -> FAIL with neutral remedy", func(t *testing.T) {
		dir := currencyRepo(t)
		path := hookPath(dir)
		require.NoError(t, os.MkdirAll(path, 0o755))
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "FAIL", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "cannot read")
		assert.Contains(t, r.Detail, "inspect the file manually")
		assert.NotContains(t, r.Detail, "check file permissions")
	})
}

// TestCheckHookCurrencyHooksPathWarning pins the LOW finding paired with
// checkHookPresence's: CheckHookCurrency also discarded githook.HooksDir's
// diverted-core.hooksPath warning with `dir, _ :=`. Currency is the other
// surface that already resolves the hooks dir closely enough to name the
// divergence.
func TestCheckHookCurrencyHooksPathWarning(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	cmd := testhelpers.GitCommand("-C", repo, "init", "-q")
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init: %s", out)
	cmd = testhelpers.GitCommand("-C", repo, "config", "core.hooksPath", ".husky")
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git config: %s", out)

	husky := filepath.Join(repo, ".husky")
	require.NoError(t, os.MkdirAll(husky, 0o755))
	_, chainErr := githook.Chain(filepath.Join(husky, currencyTestSpec.File), currencyTestSpec.Canonical, currencyTestSpec.Tag, currencyTestSpec.Ident)
	require.NoError(t, chainErr)

	r := CheckHookCurrency(repo, currencyTestSpec)
	assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
	assert.Contains(t, r.Detail, "core.hooksPath places hooks at")
	assert.Contains(t, r.Detail, "inside the work tree")
}

// TestCheckHookCurrencyCRLFHostNotStale is the CRLF regression: Chain
// rewrites a section's line endings to match a foreign CRLF host, so a
// naive byte compare would misread that EOL rewrite as drift. The
// terminator-normalized comparison must still report Current.
func TestCheckHookCurrencyCRLFHostNotStale(t *testing.T) {
	dir := currencyRepo(t)
	hookPath := filepath.Join(dir, ".git", "hooks", currencyTestSpec.File)
	require.NoError(t, os.WriteFile(hookPath, []byte("#!/bin/sh\r\nrun_something || exit 1\r\n"), 0o755))
	_, err := githook.Chain(hookPath, currencyTestSpec.Canonical, currencyTestSpec.Tag, currencyTestSpec.Ident)
	require.NoError(t, err)

	r := CheckHookCurrency(dir, currencyTestSpec)
	assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
	assert.Contains(t, r.Detail, "matches this ethos build")
}

// A hand-written or framework-managed hook that calls the public command the
// shim replaced is active, not unchained: in an enabled repo it PASSes, and in a
// dormant repo it WARNs as chained-but-not-enabled. Only a MARKER section's
// staleness is CheckHookCurrency's byte comparison.
func TestCheckHookPresenceAcceptsLegacyArgv(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  HookSpec
		file  string
		body  string
		check func(string) Result
	}{
		{"audit seal", sealHookSpec, "pre-commit", "#!/bin/sh\nethos audit seal --commit-context || exit 2\n", CheckSealHook},
		{"hook commit-trailers", trailerHookSpec, "commit-msg", "#!/bin/sh\n[ -z \"$1\" ] && exit 0\nethos hook commit-trailers\n", CheckTrailerHook},
		{"hook commit-message", trailerHookSpec, "commit-msg", "#!/bin/sh\n[ -z \"$1\" ] && exit 0\nethos hook commit-message \"$1\"\n", CheckTrailerHook},
	} {
		t.Run(tc.name+"/enabled PASSes", func(t *testing.T) {
			dir := repoTemp(t)
			hooksDir := filepath.Join(dir, ".git", "hooks")
			require.NoError(t, os.MkdirAll(hooksDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(hooksDir, tc.file), []byte(tc.body), 0o755))
			zone := filepath.Join(dir, ".punt-labs", "ethos")
			require.NoError(t, os.MkdirAll(zone, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(zone, "enabled"), nil, 0o644))
			r := tc.check(dir)
			assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		})
		t.Run(tc.name+"/dormant WARNs", func(t *testing.T) {
			dir := repoTemp(t)
			hooksDir := filepath.Join(dir, ".git", "hooks")
			require.NoError(t, os.MkdirAll(hooksDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(hooksDir, tc.file), []byte(tc.body), 0o755))
			r := tc.check(dir)
			assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
			assert.Contains(t, r.Detail, "chained but ethos not enabled here")
		})
	}
}

// TestCheckHookCurrencyShims runs CheckHookCurrency on the real DES-083 shims:
// an installed shim is current, also behind a foreign CRLF host, and a section
// copied before the shim existed is stale. The sha now moves only when the
// shim moves, so this is the migration path, not policy delivery.
func TestCheckHookCurrencyShims(t *testing.T) {
	for _, spec := range []HookSpec{sealHookSpec, trailerHookSpec} {
		t.Run(spec.ShortName+"/current", func(t *testing.T) {
			dir := currencyRepo(t)
			hookPath := filepath.Join(dir, ".git", "hooks", spec.File)
			_, err := githook.Chain(hookPath, spec.Canonical, spec.Tag, spec.Ident)
			require.NoError(t, err)
			r := CheckHookCurrency(dir, spec)
			assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
			assert.Contains(t, r.Detail, "matches this ethos build")
		})
		t.Run(spec.ShortName+"/behind a CRLF host", func(t *testing.T) {
			dir := currencyRepo(t)
			hookPath := filepath.Join(dir, ".git", "hooks", spec.File)
			require.NoError(t, os.WriteFile(hookPath, []byte("#!/bin/sh\r\nrun_something || exit 1\r\n"), 0o755))
			_, err := githook.Chain(hookPath, spec.Canonical, spec.Tag, spec.Ident)
			require.NoError(t, err)
			r := CheckHookCurrency(dir, spec)
			assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
			assert.Contains(t, r.Detail, "matches this ethos build")
		})
		t.Run(spec.ShortName+"/copied before the shim is stale", func(t *testing.T) {
			dir := currencyRepo(t)
			zone := filepath.Join(dir, ".punt-labs", "ethos")
			require.NoError(t, os.MkdirAll(zone, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(zone, "enabled"), nil, 0o644))
			hookPath := filepath.Join(dir, ".git", "hooks", spec.File)
			old := []byte("#!/bin/sh\n# " + spec.Ident + "\nethos audit seal --commit-context || exit 2\n")
			_, err := githook.Chain(hookPath, old, spec.Tag, spec.Ident)
			require.NoError(t, err)
			r := CheckHookCurrency(dir, spec)
			assert.NotEqual(t, "PASS", r.Status, "detail: %s", r.Detail)
			assert.Contains(t, r.Detail, "differs from what this ethos build would install")
		})
	}
}

// TestCheckHookCurrencyOneByteDriftIsStale is the drift-positive case: a
// single hand-edited byte inside an otherwise-valid section, simulating an
// older release's content, must report Stale with both hash prefixes in
// Detail.
func TestCheckHookCurrencyOneByteDriftIsStale(t *testing.T) {
	dir := currencyRepo(t)
	hookPath := filepath.Join(dir, ".git", "hooks", currencyTestSpec.File)
	_, err := githook.Chain(hookPath, currencyTestSpec.Canonical, currencyTestSpec.Tag, currencyTestSpec.Ident)
	require.NoError(t, err)

	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)
	edited := bytes.Replace(data, []byte("echo hello"), []byte("echo hellO"), 1)
	require.NotEqual(t, data, edited, "fixture setup: replacement did not apply")
	require.NoError(t, os.WriteFile(hookPath, edited, 0o755))

	r := CheckHookCurrency(dir, currencyTestSpec)
	assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
	assert.Contains(t, r.Detail, "installed sha256:")
	assert.Contains(t, r.Detail, "current sha256:")
}

// TestCheckHookCurrencyStaleSeverity: a stale section of a hook whose staleness
// can block commits (the seal hook) is FAIL with the commit-blocking remedy; a
// stale section of any other hook stays WARN.
func TestCheckHookCurrencyStaleSeverity(t *testing.T) {
	pre421Seal := []byte("#!/bin/sh\n# " + hooks.SealIdent + "\nethos audit seal || exit 2\n")
	pre415 := []byte("#!/bin/sh\n# " + hooks.TrailerIdent + "\n" + pre415TrailerLoop)
	tests := []struct {
		name       string
		spec       HookSpec
		file       string
		stale      []byte
		wantStatus string
		wantDetail string
	}{
		{"seal is FAIL", sealHookSpec, "pre-commit", pre421Seal, "FAIL",
			"stale section may block commits — run `ethos enable` (SessionStart also refreshes it)"},
		{"trailer stays WARN", trailerHookSpec, "commit-msg", pre415, "WARN",
			"run `ethos enable` to refresh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := currencyRepo(t)
			zone := filepath.Join(dir, ".punt-labs", "ethos") // enabled: a stale seal blocks commits
			require.NoError(t, os.MkdirAll(zone, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(zone, "enabled"), nil, 0o644))
			hookPath := filepath.Join(dir, ".git", "hooks", tt.file)
			_, err := githook.Chain(hookPath, tt.stale, tt.spec.Tag, tt.spec.Ident)
			require.NoError(t, err)

			r := CheckHookCurrency(dir, tt.spec)
			assert.Equal(t, tt.wantStatus, r.Status, "detail: %s", r.Detail)
			assert.Contains(t, r.Detail, tt.wantDetail)
			assert.Contains(t, r.Detail, "differs from what this ethos build would install")
		})
	}
}

// A stale seal section in a repo where ethos is not enabled passes through, so
// it is a WARN; enabled, it blocks commits and is a FAIL.
func TestCheckHookCurrencyStaleSealDormantIsWarn(t *testing.T) {
	pre421Seal := []byte("#!/bin/sh\n# " + hooks.SealIdent + "\nethos audit seal || exit 2\n")
	dir := currencyRepo(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	_, err := githook.Chain(hookPath, pre421Seal, sealHookSpec.Tag, sealHookSpec.Ident)
	require.NoError(t, err)

	r := CheckHookCurrency(dir, sealHookSpec)
	assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
	assert.Contains(t, r.Detail, "dormant repo: hook passes through")

	zone := filepath.Join(dir, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(zone, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(zone, "enabled"), nil, 0o644))
	assert.Equal(t, "FAIL", CheckHookCurrency(dir, sealHookSpec).Status)
}

// pre415TrailerLoop is the pre-#415 commit-msg session-resolution loop,
// quoted verbatim from docs/design-hook-drift-detection.md's Problem
// section: the reverse-sort fallback PR #415 (5b80bff, merged 2026-07-31)
// replaced with a call to `ethos hook commit-trailers`. ethos-pobi found this
// exact text still running in punt-kit nine days after the fix merged — the
// incident CheckHookCurrency exists to catch.
const pre415TrailerLoop = `  for d in $(find "$HOME/.punt-labs/ethos/sessions" -maxdepth 1 -type d 2>/dev/null | sort -r); do
    if [ -f "$d/delegation-binding" ]; then
      binding_file="$d/delegation-binding"
      break
    fi
  done
`

// TestCheckHookCurrencyPuntKitRegression pins the ethos-pobi incident as a
// regression test: a hook chained with the pre-#415 body must report Stale
// against today's hooks.CommitMsg, or the drift-detection mechanism this
// design adds has itself regressed.
func TestCheckHookCurrencyPuntKitRegression(t *testing.T) {
	dir := currencyRepo(t)
	hookPath := filepath.Join(dir, ".git", "hooks", "commit-msg")
	pre415 := []byte("#!/bin/sh\n# " + hooks.TrailerIdent + "\n" + pre415TrailerLoop)
	_, err := githook.Chain(hookPath, pre415, hooks.TrailerTag, hooks.TrailerIdent)
	require.NoError(t, err)

	r := CheckHookCurrency(dir, trailerHookSpec)
	assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
	assert.Contains(t, r.Detail, "differs from what this ethos build would install")
}

// TestRunAllHookCurrencyIndependentOfEnabledMarker: a dormant repo (no
// enabled marker) with a leftover chained-but-current hook must still report
// PASS on both currency checks — RunAll never gates CheckHookCurrency on the
// enabled marker the way CheckSealHook does.
func TestRunAllHookCurrencyIndependentOfEnabledMarker(t *testing.T) {
	s, ss, root := newFixture(t)
	writeIdentity(t, root, "mal", "name: Mal\nhandle: mal\nkind: human\n")
	t.Setenv("USER", "mal")
	t.Setenv("HOME", t.TempDir())

	repo := repoTemp(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755))
	_, err := githook.Chain(filepath.Join(repo, ".git", "hooks", "pre-commit"), hooks.PreCommit, hooks.SealTag, hooks.SealIdent)
	require.NoError(t, err)
	_, err = githook.Chain(filepath.Join(repo, ".git", "hooks", "commit-msg"), hooks.CommitMsg, hooks.TrailerTag, hooks.TrailerIdent)
	require.NoError(t, err)
	// No enabled marker written — the repo stays dormant.

	results := RunAll(s, ss, repo, nil, repo, nil, nil)
	var sealCurrency, trailerCurrency *Result
	for i := range results {
		switch results[i].Name {
		case "Seal hook currency":
			sealCurrency = &results[i]
		case "Trailer hook currency":
			trailerCurrency = &results[i]
		}
	}
	require.NotNil(t, sealCurrency, "results: %+v", results)
	require.NotNil(t, trailerCurrency, "results: %+v", results)
	assert.Equal(t, "PASS", sealCurrency.Status, "detail: %s", sealCurrency.Detail)
	assert.Equal(t, "PASS", trailerCurrency.Status, "detail: %s", trailerCurrency.Detail)
}

func TestRunAllAndHelpers(t *testing.T) {
	// A fixture that passes all four checks initially, including
	// human-identity via USER=mal matching the mal identity.
	s, ss, root := newFixture(t)
	writeIdentity(t, root, "mal",
		"name: Mal\nhandle: mal\nkind: human\n")

	t.Setenv("USER", "mal")
	t.Setenv("HOME", t.TempDir())

	// CheckDefaultAgent walks up from CWD looking for .git. Put us in a
	// fresh temp dir that is definitely not inside a git repo — a repo
	// ancestor would cause the walk to find .git several directories up
	// and trigger a non-deterministic result.
	t.Chdir(outsideRepoTempDir(t))

	// Pass empty repoRoot/storeRoot and nil teams — the orphaned-agent
	// check degrades to PASS ("not in a repo") in this configuration.
	results := RunAll(s, ss, "", nil, "", nil, nil)
	require.Len(t, results, 19)

	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}
	assert.Equal(t, []string{
		"Identity directory",
		"Human identity",
		"Default agent",
		"Duplicate fields",
		"Orphaned agent files",
		"Agent file currency",
		"Team members",
		"Audit seal hook",
		"Audit trailer hook",
		"Seal hook currency",
		"Trailer hook currency",
		"Hook binary",
		"Repo-only completeness",
		"Local extension files",
		"Extension key names",
		"Mission file hand-edits",
		"Code archetype delegated-worker guard",
		"Unclassified files",
		"Physical storage placement",
	}, names)

	assert.True(t, AllPassed(results), "results: %+v", results)
	assert.Equal(t, 19, PassedCount(results))

	// Now inject a failure: remove the identities directory. RunAll
	// should report at least one failure and AllPassed should flip.
	require.NoError(t, os.RemoveAll(filepath.Join(root, "identities")))
	results = RunAll(s, ss, "", nil, "", nil, nil)
	assert.False(t, AllPassed(results))
	assert.Less(t, PassedCount(results), 18)

	// At least one result should name the identity directory failure.
	var found bool
	for _, r := range results {
		if r.Name == "Identity directory" && !r.Passed() {
			found = true
			assert.True(t, strings.Contains(r.Detail, "not found") ||
				strings.Contains(r.Detail, "error"))
		}
	}
	assert.True(t, found)
}

// TestCheckOrphanedAgentFiles_ResolvesTeamFromStoreRoot pins the Bugbot #370
// class in doctor: the .claude/agents glob is per-checkout (repoRoot), but the
// active-team read must resolve from the shared store (storeRoot) — the tree
// where `ethos team activate` wrote it and every other reader resolves it.
// Resolving the team from the checkout instead misclassifies agents as
// orphaned when a worktree's ethos.yaml names a different team than the store.
func TestCheckOrphanedAgentFiles_ResolvesTeamFromStoreRoot(t *testing.T) {
	// checkoutRoot holds the per-checkout agent file, and its OWN config
	// names a team (nobwk) that does NOT include bwk.
	checkoutRoot := t.TempDir()
	agentsDir := filepath.Join(checkoutRoot, ".claude", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "bwk.md"), []byte("# bwk\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(checkoutRoot, ".punt-labs"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(checkoutRoot, ".punt-labs", "ethos.yaml"), []byte("team: nobwk\n"), 0o644))

	// storeRoot is where activation wrote team: withbwk, whose members
	// include bwk. Both team definitions live in the store's team dir.
	storeRoot := t.TempDir()
	ethosDir := filepath.Join(storeRoot, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "teams"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(storeRoot, ".punt-labs", "ethos.yaml"), []byte("team: withbwk\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "teams", "withbwk.yaml"),
		[]byte("name: withbwk\nmembers:\n  - identity: bwk\n    role: go-specialist\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "teams", "nobwk.yaml"),
		[]byte("name: nobwk\nmembers:\n  - identity: someone\n    role: other\n"), 0o644))
	teams := team.NewLayeredStore(ethosDir, ethosDir)

	// Team resolved from the store (withbwk, has bwk) → bwk not orphaned.
	res := CheckOrphanedAgentFiles(checkoutRoot, storeRoot, teams, nil)
	assert.Equal(t, "PASS", res.Status,
		"bwk is on the store's active team; must not be flagged orphaned: %+v", res)

	// Regression guard: resolving the team from the checkout (nobwk, lacks
	// bwk) misclassifies bwk as orphaned — the bug this split fixes.
	buggy := CheckOrphanedAgentFiles(checkoutRoot, checkoutRoot, teams, nil)
	assert.Equal(t, "FAIL", buggy.Status,
		"resolving the team from the checkout root falsely flags bwk as orphaned")
}

// TestCheckOrphanedAgentFiles_ChecklistAgents pins the DES-070 exemption:
// the three seeded review-checklist agents (code-reviewer,
// silent-failure-hunter, invariant-completeness-reviewer) carry no team
// membership by design and must not be flagged orphaned, while a genuine
// orphan alongside them is still caught.
func TestCheckOrphanedAgentFiles_ChecklistAgents(t *testing.T) {
	cases := []struct {
		name       string
		agentFiles []string
		wantStatus string
		wantDetail string
	}{
		{
			name:       "a seeded checklist agent alone passes",
			agentFiles: []string{"code-reviewer.md"},
			wantStatus: "PASS",
		},
		{
			name:       "all three seeded checklist agents pass",
			agentFiles: []string{"code-reviewer.md", "silent-failure-hunter.md", "invariant-completeness-reviewer.md"},
			wantStatus: "PASS",
		},
		{
			name:       "a genuine orphan alongside a checklist agent is still flagged",
			agentFiles: []string{"code-reviewer.md", "not-a-real-handle.md"},
			wantStatus: "FAIL",
			wantDetail: "not-a-real-handle",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkoutRoot := t.TempDir()
			agentsDir := filepath.Join(checkoutRoot, ".claude", "agents")
			require.NoError(t, os.MkdirAll(agentsDir, 0o755))
			for _, f := range tc.agentFiles {
				require.NoError(t, os.WriteFile(filepath.Join(agentsDir, f), []byte("---\nname: x\n---\nbody\n"), 0o644))
			}
			require.NoError(t, os.MkdirAll(filepath.Join(checkoutRoot, ".punt-labs"), 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(checkoutRoot, ".punt-labs", "ethos.yaml"), []byte("team: solo\n"), 0o644))

			ethosDir := filepath.Join(checkoutRoot, ".punt-labs", "ethos")
			require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "teams"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "teams", "solo.yaml"),
				[]byte("name: solo\nmembers:\n  - identity: someone-else\n    role: other\n"), 0o644))
			teams := team.NewLayeredStore(ethosDir, ethosDir)

			res := CheckOrphanedAgentFiles(checkoutRoot, checkoutRoot, teams, nil)
			assert.Equal(t, tc.wantStatus, res.Status, "detail: %s", res.Detail)
			if tc.wantDetail != "" {
				assert.Contains(t, res.Detail, tc.wantDetail)
			}
		})
	}
}

// TestCheckOrphanedAgentFiles_Classification pins ethos-jw1z: the FAIL
// detail must distinguish a handle with a resolvable identity (stale —
// generated for a previous team scope, safe to delete) from a handle
// matching no identity anywhere (a genuine orphan, worth investigating).
// Pre-fix, both read identically as "orphaned agent files (not on any
// team): <handles>", giving the operator no way to tell which is which
// without reading source.
func TestCheckOrphanedAgentFiles_Classification(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
	s := identity.NewStore(root)

	// "retired" resolves to a real identity — it was clearly generated for
	// some team at some point, just not the currently active one.
	writeIdentity(t, root, "retired", "name: Retired\nhandle: retired\nkind: agent\n")

	// "wounded" has a matching identity file that exists but is unreadable
	// (N1): classification must still call it "stale", not fall into a
	// third bucket or crash trying to parse it — s.Exists is a bare
	// os.Stat, unaffected by the FILE's own permission bits (only the
	// containing directory's search permission matters), and deliberately
	// never calls s.Load here, which would both fail on this file AND, for
	// an ordinary readable legacy identity, silently RE-SAVE it via
	// migrateVoice — a write this read-only health check must never cause.
	// Skipped as root, which bypasses permission bits, and on Windows, where
	// chmod 0o000 only sets the read-only attribute: the file stays readable,
	// so the "unreadable" premise never holds, and the Perm() assertion below
	// would read 0o444 rather than 0o000. os.Geteuid alone does not cover
	// that — it returns -1 on Windows, never 0.
	haveWounded := os.Geteuid() != 0 && runtime.GOOS != "windows"
	if haveWounded {
		writeIdentity(t, root, "wounded", "name: Wounded\nhandle: wounded\nkind: agent\n")
		woundedPath := filepath.Join(root, "identities", "wounded.yaml")
		require.NoError(t, os.Chmod(woundedPath, 0o000))
		t.Cleanup(func() { _ = os.Chmod(woundedPath, 0o600) })
	}

	agentsDir := filepath.Join(root, ".claude", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "retired.md"), []byte("---\nname: x\n---\nbody\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "phantom.md"), []byte("---\nname: x\n---\nbody\n"), 0o644))
	if haveWounded {
		require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "wounded.md"), []byte("---\nname: x\n---\nbody\n"), 0o644))
	}

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("team: solo\n"), 0o644))
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "teams"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "teams", "solo.yaml"),
		[]byte("name: solo\nmembers:\n  - identity: someone-else\n    role: other\n"), 0o644))
	teams := team.NewLayeredStore(ethosDir, ethosDir)

	t.Run("with an identity store, stale fails and unresolved is ignored", func(t *testing.T) {
		res := CheckOrphanedAgentFiles(root, root, teams, s)
		assert.Equal(t, "FAIL", res.Status)
		assert.Contains(t, res.Detail, "stale", "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "retired", "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "safe to delete", "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "not ethos-generated, ignored: phantom", "detail: %s", res.Detail)
		assert.NotContains(t, res.Detail, "investigate", "detail: %s", res.Detail)
		if haveWounded {
			// N1: an unreadable-but-present identity file still classifies
			// as stale (a file IS there), never crashing into an error path
			// and never landing in the genuine-absence bucket.
			assert.Contains(t, res.Detail, "stale (known identity, not on the active team", "detail: %s", res.Detail)
			assert.Contains(t, res.Detail, "wounded", "detail: %s", res.Detail)
			assert.NotContains(t, res.Detail, "ignored: wounded",
				"an unreadable-but-present identity file must not be folded into the genuine-absence bucket: %s", res.Detail)
			woundedPath := filepath.Join(root, "identities", "wounded.yaml")
			info, statErr := os.Stat(woundedPath)
			require.NoError(t, statErr)
			assert.Equal(t, os.FileMode(0o000), info.Mode().Perm(),
				"classification must not have touched the file's permissions or content")
		}
	})

	// N1: CheckOrphanedAgentFiles is a read-only health check. Pre-fix, its
	// classification called s.Load, which — for an identity carrying a
	// legacy voice: key — migrates it to ext/vox and RE-SAVES the identity
	// file, mutating the file an operator asked doctor to merely inspect.
	t.Run("classification never rewrites a legacy identity file (N1)", func(t *testing.T) {
		legacyRoot := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(legacyRoot, "identities"), 0o700))
		legacyS := identity.NewStore(legacyRoot)
		legacyBody := "name: Legacy\nhandle: legacy\nkind: agent\nvoice:\n  provider: elevenlabs\n  voice_id: abc123\n"
		writeIdentity(t, legacyRoot, "legacy", legacyBody)

		agentsDir := filepath.Join(legacyRoot, ".claude", "agents")
		require.NoError(t, os.MkdirAll(agentsDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "legacy.md"), []byte("---\nname: x\n---\nbody\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(legacyRoot, ".punt-labs"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(legacyRoot, ".punt-labs", "ethos.yaml"), []byte("team: solo\n"), 0o644))
		legacyEthosDir := filepath.Join(legacyRoot, ".punt-labs", "ethos")
		require.NoError(t, os.MkdirAll(filepath.Join(legacyEthosDir, "teams"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(legacyEthosDir, "teams", "solo.yaml"),
			[]byte("name: solo\nmembers:\n  - identity: someone-else\n    role: other\n"), 0o644))
		legacyTeams := team.NewLayeredStore(legacyEthosDir, legacyEthosDir)

		res := CheckOrphanedAgentFiles(legacyRoot, legacyRoot, legacyTeams, legacyS)
		assert.Equal(t, "FAIL", res.Status)
		assert.Contains(t, res.Detail, "stale", "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "legacy", "detail: %s", res.Detail)

		after, err := os.ReadFile(filepath.Join(legacyRoot, "identities", "legacy.yaml"))
		require.NoError(t, err)
		assert.Equal(t, legacyBody, string(after),
			"CheckOrphanedAgentFiles must never rewrite an identity file — it is a diagnostic, not a migration")
		_, extErr := os.Stat(filepath.Join(legacyRoot, "identities", "legacy.ext", "vox.yaml"))
		assert.True(t, os.IsNotExist(extErr),
			"classification must not trigger the voice-to-ext migration as a side effect")
	})

	// A handle matching no identity cannot have been generated by ethos: a
	// hand-authored agent (biff alex-chen) must not fail doctor.
	t.Run("only unresolved handles pass with a note", func(t *testing.T) {
		r := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(r, "identities"), 0o700))
		a := filepath.Join(r, ".claude", "agents")
		require.NoError(t, os.MkdirAll(a, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(a, "alex-chen.md"), []byte("---\nname: x\n---\nbody\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(r, ".punt-labs"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(r, ".punt-labs", "ethos.yaml"), []byte("team: solo\n"), 0o644))
		ed := filepath.Join(r, ".punt-labs", "ethos")
		require.NoError(t, os.MkdirAll(filepath.Join(ed, "teams"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ed, "teams", "solo.yaml"),
			[]byte("name: solo\nmembers:\n  - identity: someone-else\n    role: other\n"), 0o644))

		res := CheckOrphanedAgentFiles(r, r, team.NewLayeredStore(ed, ed), identity.NewStore(r))
		assert.Equal(t, "PASS", res.Status, "detail: %s", res.Detail)
		assert.Equal(t, "no orphaned agent files (not ethos-generated, ignored: alex-chen)", res.Detail)
	})

	t.Run("only stale handles fail with no ignored note", func(t *testing.T) {
		status, detail := classifyOrphans([]orphanFile{{handle: "retired"}}, s)
		assert.Equal(t, "FAIL", status)
		assert.Equal(t, "orphaned agent files — stale (known identity, not on the active team — generated for a previous team scope, safe to delete): retired", detail)
	})

	// ethos never deletes agent files it wrote, and `vendor --prune` removes
	// identities, so a marked file with no identity is a leftover of ours.
	t.Run("marked unresolved fails, unmarked unresolved is ignored", func(t *testing.T) {
		r := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(r, "identities"), 0o700))
		a := filepath.Join(r, ".claude", "agents")
		require.NoError(t, os.MkdirAll(a, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(a, "gone.md"), []byte(agentmark.Add("---\nname: gone\n---\nbody\n")), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(a, "alex-chen.md"), []byte("---\nname: x\n---\nbody\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(r, ".punt-labs"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(r, ".punt-labs", "ethos.yaml"), []byte("team: solo\n"), 0o644))
		ed := filepath.Join(r, ".punt-labs", "ethos")
		require.NoError(t, os.MkdirAll(filepath.Join(ed, "teams"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ed, "teams", "solo.yaml"),
			[]byte("name: solo\nmembers:\n  - identity: someone-else\n    role: other\n"), 0o644))
		tm := team.NewLayeredStore(ed, ed)

		res := CheckOrphanedAgentFiles(r, r, tm, identity.NewStore(r))
		assert.Equal(t, "FAIL", res.Status)
		assert.Equal(t, "orphaned agent files — generated by ethos for an identity that no longer exists — safe to delete: gone (not ethos-generated, ignored: alex-chen)", res.Detail)

		// Only a marked leftover, no hand-authored file.
		require.NoError(t, os.Remove(filepath.Join(a, "alex-chen.md")))
		res = CheckOrphanedAgentFiles(r, r, tm, identity.NewStore(r))
		assert.Equal(t, "FAIL", res.Status)
		assert.NotContains(t, res.Detail, "ignored")

		// Only the hand-authored file.
		require.NoError(t, os.Remove(filepath.Join(a, "gone.md")))
		require.NoError(t, os.WriteFile(filepath.Join(a, "alex-chen.md"), []byte("---\nname: x\n---\nbody\n"), 0o644))
		res = CheckOrphanedAgentFiles(r, r, tm, identity.NewStore(r))
		assert.Equal(t, "PASS", res.Status)
	})

	t.Run("a marked file with a known identity is stale, not removed", func(t *testing.T) {
		status, detail := classifyOrphans([]orphanFile{{handle: "retired", marked: true}}, s)
		assert.Equal(t, "FAIL", status)
		assert.Contains(t, detail, "stale")
		assert.NotContains(t, detail, "no longer exists")
	})

	t.Run("without an identity store, the distinction is not guessed", func(t *testing.T) {
		res := CheckOrphanedAgentFiles(root, root, teams, nil)
		assert.Equal(t, "FAIL", res.Status)
		assert.Contains(t, res.Detail, "retired")
		assert.Contains(t, res.Detail, "phantom")
		assert.NotContains(t, res.Detail, "stale", "no identity store means no basis to classify — must not guess")
	})
}

// TestCheckOrphanedAgentFiles_ErrorPathsFail pins M3: four PASS-on-error
// paths in CheckOrphanedAgentFiles read a real fault as "nothing to check",
// leaving a green column over an invariant that was never actually verified
// — the same shape ethos-e05k names. checklistAgentNames' own broken-embed
// handling a few lines below (doctor.go's FAIL for a build-time defect) is
// the precedent these four should have followed from the start.
func TestCheckOrphanedAgentFiles_ErrorPathsFail(t *testing.T) {
	t.Run("malformed glob pattern FAILs, not PASS nothing to check", func(t *testing.T) {
		// An unterminated '[' character class makes filepath.Glob return
		// ErrBadPattern for any pattern built under this root.
		repoRoot := filepath.Join(t.TempDir(), "repo[unterminated")
		require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".claude", "agents"), 0o755))

		res := CheckOrphanedAgentFiles(repoRoot, repoRoot, nil, nil)
		assert.Equal(t, "FAIL", res.Status, "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "could not glob agents")
	})

	t.Run("malformed repo config FAILs, not PASS nothing to check", func(t *testing.T) {
		root := t.TempDir()
		agentsDir := filepath.Join(root, ".claude", "agents")
		require.NoError(t, os.MkdirAll(agentsDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "bwk.md"), []byte("# bwk\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("team: [not a string"), 0o644))

		res := CheckOrphanedAgentFiles(root, root, nil, nil)
		assert.Equal(t, "FAIL", res.Status, "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "could not load repo config")
	})

	t.Run("nil team store with a configured team FAILs, not PASS nothing to check", func(t *testing.T) {
		root := t.TempDir()
		agentsDir := filepath.Join(root, ".claude", "agents")
		require.NoError(t, os.MkdirAll(agentsDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "bwk.md"), []byte("# bwk\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("team: solo\n"), 0o644))

		res := CheckOrphanedAgentFiles(root, root, nil, nil)
		assert.Equal(t, "FAIL", res.Status, "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "no team store available")
	})

	t.Run("malformed team file FAILs, not PASS nothing to check", func(t *testing.T) {
		root := t.TempDir()
		agentsDir := filepath.Join(root, ".claude", "agents")
		require.NoError(t, os.MkdirAll(agentsDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "bwk.md"), []byte("# bwk\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("team: solo\n"), 0o644))
		ethosDir := filepath.Join(root, ".punt-labs", "ethos")
		require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "teams"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "teams", "solo.yaml"),
			[]byte("members: [not a string"), 0o644))
		teams := team.NewLayeredStore(ethosDir, ethosDir)

		res := CheckOrphanedAgentFiles(root, root, teams, nil)
		assert.Equal(t, "FAIL", res.Status, "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, `could not load team "solo"`)
	})
}

// TestCheckOrphanedAgentFiles_InstallerOwned: a handle with a source file in
// .punt-labs/ethos/agents/ is re-copied every SessionStart, so deleting it is
// undone; it must never FAIL, whether it has no identity (utility agent) or
// an off-team one.
func TestCheckOrphanedAgentFiles_InstallerOwned(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
	writeIdentity(t, root, "offteam", "name: Off\nhandle: offteam\nkind: agent\n")
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "teams"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("team: solo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "teams", "solo.yaml"),
		[]byte("name: solo\nmembers:\n  - identity: someone-else\n    role: other\n"), 0o644))
	out := filepath.Join(root, ".claude", "agents")
	require.NoError(t, os.MkdirAll(out, 0o755))
	for _, h := range []string{"utility", "offteam"} {
		src := "---\nname: " + h + "\n---\nbody\n"
		require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "agents", h+".md"), []byte(src), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(out, h+".md"), []byte(agentmark.Add(src)), 0o644))
	}
	res := CheckOrphanedAgentFiles(root, root, team.NewLayeredStore(ethosDir, ethosDir), identity.NewStore(root))
	assert.Equal(t, "PASS", res.Status, "detail: %s", res.Detail)
	assert.Equal(t, "no orphaned agent files", res.Detail)
}

// An agents source dir that cannot be searched leaves ownership unknown; the
// check must say so rather than classify the file.
func TestCheckOrphanedAgentFiles_SourceStatError(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("permission bits do not bind root or windows")
	}
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
	writeIdentity(t, root, "offteam", "name: Off\nhandle: offteam\nkind: agent\n")
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")
	agentsSrc := filepath.Join(ethosDir, "agents")
	require.NoError(t, os.MkdirAll(agentsSrc, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "teams"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("team: solo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "teams", "solo.yaml"),
		[]byte("name: solo\nmembers:\n  - identity: someone-else\n    role: other\n"), 0o644))
	out := filepath.Join(root, ".claude", "agents")
	require.NoError(t, os.MkdirAll(out, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "offteam.md"), []byte(agentmark.Add("---\nname: offteam\n---\n")), 0o644))
	require.NoError(t, os.Chmod(agentsSrc, 0o000))
	t.Cleanup(func() { _ = os.Chmod(agentsSrc, 0o755) })

	res := CheckOrphanedAgentFiles(root, root, team.NewLayeredStore(ethosDir, ethosDir), identity.NewStore(root))
	assert.Equal(t, "FAIL", res.Status)
	assert.Contains(t, res.Detail, "could not stat agent source")
	assert.NotContains(t, res.Detail, "safe to delete")
}

// brokenFS.ReadDir always fails, simulating a build-broken embed.
type brokenFS struct{}

func (brokenFS) Open(string) (fs.File, error) {
	return nil, errors.New("broken fs")
}

// TestChecklistAgentNames_ReadError pins the fix for the silent-failure
// finding: a ReadDir failure must surface as an error the caller reports
// plainly, not be swallowed into an empty set that masquerades as "no
// exemptions" and produces a misleading orphan FAIL.
func TestChecklistAgentNames_ReadError(t *testing.T) {
	names, err := checklistAgentNames(brokenFS{}, "sidecar/agents")
	require.Error(t, err)
	assert.Nil(t, names)
}

// TestChecklistAgentNames_Real pins the production call site against the
// actual embedded seed.Agents: the three seeded handles resolve, and the
// name set can never drift from what `ethos seed` deploys because both
// read the same embed.FS.
func TestChecklistAgentNames_Real(t *testing.T) {
	names, err := checklistAgentNames(seed.Agents, "sidecar/agents")
	require.NoError(t, err)
	assert.Contains(t, names, "code-reviewer")
	assert.Contains(t, names, "silent-failure-hunter")
	assert.Contains(t, names, "invariant-completeness-reviewer")
	assert.NotContains(t, names, "README",
		"README.md must not be treated as an exempted agent handle")
}

// TestHookChecksOnADegradedLookup: with git off PATH core.hooksPath cannot be
// read, so no hook check may report a verdict on the directory it guessed. An
// enabled repo whose hooks cannot be verified FAILs the presence check (a
// green line would hide an unsealed repo); a dormant one WARNs, since a stale
// chained hook in the directory git really runs would stay hidden; the currency
// check only WARNs.
func TestHookChecksOnADegradedLookup(t *testing.T) {
	enabled := repoTemp(t)
	require.NoError(t, os.MkdirAll(filepath.Join(enabled, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(enabled, ".punt-labs", "ethos", "enabled"), nil, 0o644))
	dormant := repoTemp(t)
	t.Setenv("PATH", t.TempDir()) // no git

	r := CheckSealHook(enabled)
	assert.Equal(t, "FAIL", r.Status, r.Detail)
	assert.Contains(t, r.Detail, "cannot verify hooks:")
	assert.Contains(t, r.Detail, "install git")

	r = CheckTrailerHook(enabled)
	assert.Equal(t, "FAIL", r.Status, r.Detail)

	r = CheckSealHook(dormant)
	assert.Equal(t, "WARN", r.Status, r.Detail)
	assert.True(t, strings.HasPrefix(r.Detail, "cannot verify hooks:"), r.Detail)
	assert.Contains(t, r.Detail, "git rev-parse")

	for name, dir := range map[string]string{"enabled": enabled, "dormant": dormant} {
		r := CheckHookCurrency(dir, currencyTestSpec)
		assert.Equal(t, "WARN", r.Status, "%s: %s", name, r.Detail)
		assert.Contains(t, r.Detail, "hooks directory not verified:", name)
		assert.Contains(t, r.Detail, "install git", "currency: %s", name)

		r = CheckUnclassifiedFiles(dir)
		assert.Equal(t, "WARN", r.Status, "%s: %s", name, r.Detail)
		assert.Contains(t, r.Detail, "hooks directory not verified:", name)
		assert.Contains(t, r.Detail, "install git", "unclassified: %s", name)
	}
}

// TestHookChecksAdviceFollowsCause: with git installed, a root git reports as
// part of another repository is told to run from its top level, never to
// install git.
func TestHookChecksAdviceFollowsCause(t *testing.T) {
	parent := repoTemp(t)
	sub := filepath.Join(parent, "sub")
	require.NoError(t, os.MkdirAll(filepath.Join(sub, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, ".punt-labs", "ethos", "enabled"), nil, 0o644))

	r := CheckSealHook(sub)
	assert.Equal(t, "FAIL", r.Status, r.Detail)
	assert.Contains(t, r.Detail, "top level")
	assert.NotContains(t, r.Detail, "install git")

	for name, r := range map[string]Result{
		"currency":     CheckHookCurrency(sub, currencyTestSpec),
		"unclassified": CheckUnclassifiedFiles(sub),
	} {
		assert.Contains(t, r.Detail, "top level", name)
		assert.NotContains(t, r.Detail, "install git", name)
	}
}

// TestRunAllRefusedRootOverride: a refused ETHOS_REPO_ROOT is a WARN on every
// check that reads the checkout, where being outside a repository stays PASS.
func TestRunAllRefusedRootOverride(t *testing.T) {
	s, ss, root := newFixture(t)
	writeIdentity(t, root, "mal", "name: Mal\nhandle: mal\nkind: human\n")
	t.Setenv("USER", "mal")
	t.Setenv("HOME", t.TempDir())

	checkout := []string{
		"Default agent", "Orphaned agent files", "Audit seal hook", "Audit trailer hook",
		"Seal hook currency", "Trailer hook currency", "Local extension files", "Unclassified files",
	}
	find := func(results []Result, name string) Result {
		for _, r := range results {
			if r.Name == name {
				return r
			}
		}
		t.Fatalf("no result named %q in %+v", name, results)
		return Result{}
	}

	refused := fmt.Errorf("ETHOS_REPO_ROOT=%q is not an existing directory; refusing to use it", "/nope")
	results := RunAll(s, ss, "", refused, "", nil, nil)
	for _, name := range checkout {
		r := find(results, name)
		assert.Equal(t, "WARN", r.Status, "%s: %s", name, r.Detail)
		assert.Contains(t, r.Detail, "ETHOS_REPO_ROOT", name)
	}

	outside := fmt.Errorf("/ is %w", resolve.ErrNotInRepo)
	results = RunAll(s, ss, "", outside, "", nil, nil)
	for _, name := range checkout {
		assert.Equal(t, "PASS", find(results, name).Status, name)
	}
	assert.Equal(t, "not scanned: not in a repository", find(results, "Unclassified files").Detail)
}

// TestRunAllRefusedStore: when the store could not be located for a reason
// other than "not in a repository", doctor emits one FAIL row naming the
// reason, and every check that read the store keeps its own status with a
// note that it saw the global layer only. Being outside a repository stays
// PASS "not in a repo", with no row and no note.
func TestRunAllRefusedStore(t *testing.T) {
	s, ss, root := newFixture(t)
	writeIdentity(t, root, "mal", "name: Mal\nhandle: mal\nkind: human\n")
	t.Setenv("USER", "mal")
	t.Setenv("HOME", t.TempDir())

	storeChecks := []string{
		"Repo-only completeness", "Mission file hand-edits", "Code archetype delegated-worker guard",
		"Identity directory", "Human identity", "Duplicate fields", "Extension key names",
	}
	find := func(results []Result, name string) Result {
		for _, r := range results {
			if r.Name == name {
				return r
			}
		}
		t.Fatalf("no result named %q in %+v", name, results)
		return Result{}
	}

	outside := fmt.Errorf("/ is %w", resolve.ErrNotInRepo)
	base := RunAll(s, ss, "", nil, "", outside, nil)
	for _, r := range base {
		assert.NotEqual(t, "Repository store", r.Name, "no row outside a repository")
		assert.NotContains(t, r.Detail, "global layer only", r.Name)
	}
	for _, name := range []string{"Repo-only completeness", "Mission file hand-edits", "Code archetype delegated-worker guard"} {
		r := find(base, name)
		assert.Equal(t, "PASS", r.Status, name)
		assert.Equal(t, "not in a repo", r.Detail, name)
	}

	refused := fmt.Errorf("ETHOS_REPO_ROOT=%q has no .punt-labs/ethos store; refusing to use it", "/nope")
	results := RunAll(s, ss, "", nil, "", refused, nil)
	row := find(results, "Repository store")
	assert.Equal(t, "FAIL", row.Status)
	assert.Equal(t, "not located: "+refused.Error(), row.Detail)
	assert.True(t, AnyFailed(results), "doctor exits non-zero when every command would refuse")
	for _, name := range storeChecks {
		r, b := find(results, name), find(base, name)
		if b.Status == "PASS" {
			// It saw the global layer only, or examined nothing: it did not check
			// the repository.
			assert.Equal(t, "WARN", r.Status, name)
			assert.Equal(t, "not checked: the repository store could not be located: "+refused.Error(), r.Detail, name)
			continue
		}
		assert.Equal(t, b.Status, r.Status, "%s keeps its status: %s", name, r.Detail)
		assert.Equal(t, b.Detail+" (global layer only; repo store not located: "+refused.Error()+")", r.Detail, name)
	}
}

// A FAIL from a check is never lowered by a gate: the store gate notes the
// global layer, the checkout gate notes the unlocated checkout.
func TestRunAllGateKeepsFail(t *testing.T) {
	s, ss, root := newFixture(t)
	writeIdentity(t, root, "mal", "name: Mal\nhandle: mal\nkind: human\nemail: x@example.com\n")
	writeIdentity(t, root, "zoe", "name: Zoe\nhandle: zoe\nkind: human\nemail: x@example.com\n")
	t.Setenv("USER", "mal")
	t.Setenv("HOME", t.TempDir())

	refused := errors.New("git cannot resolve the repository")
	for _, r := range RunAll(s, ss, "", refused, "", refused, nil) {
		if r.Name == "Duplicate fields" {
			assert.Equal(t, "FAIL", r.Status, r.Detail)
			assert.Contains(t, r.Detail, "global layer only")
		}
	}

	failed := Result{Name: "x", Status: "FAIL", Detail: "broken"}
	got := gateCheckout(failed, "", refused)
	assert.Equal(t, "FAIL", got.Status)
	assert.Equal(t, "broken (checkout not located: git cannot resolve the repository)", got.Detail)
	got = gateCheckout(Result{Name: "x", Status: "PASS", Detail: "fine"}, "", refused)
	assert.Equal(t, "WARN", got.Status)
	assert.Equal(t, "not checked: git cannot resolve the repository", got.Detail)
	got = gateStore(failed, "", refused)
	assert.Equal(t, "FAIL", got.Status)
}

// TestRunAllGatesEveryLocatedCheck audits the current runAll call structure.
// It follows its named root/store parameters through local aliases and common
// path expressions, and direct calls through doctor/githook to resolve.Roots or
// resolve.CheckoutOf. This is a bounded syntax tripwire, not a proof for arbitrary
// future Go code; gateAudit and locatingFuncs document its limits. Runtime gate
// tests above remain necessary to check the gates' behavior.
func TestRunAllGatesEveryLocatedCheck(t *testing.T) {
	runAll := parseRunAll(t, "doctor.go")
	locating := locatingFuncs(t, map[string]string{"doctor": ".", "githook": filepath.Join("..", "githook")})
	gated, ungated := gateAudit(runAll, locating)
	assert.Empty(t, ungated, "checks given a located input, or locating a repository themselves, outside checkout(...) or store(...)")
	assert.GreaterOrEqual(t, len(gated), 13, "the scan found too few gated checks: %v", gated)
}

// The audit catches a check that locates a repository through a chain of
// helpers ending in githook, and accepts the same call inside a gate.
func TestGateAuditCatchesTransitiveLocator(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(src), 0o644))
	}
	write("doctor/doctor.go", `package doctor
import "github.com/punt-labs/ethos/v4/internal/githook"

func runAll(repoRoot string) []Result {
	var results []Result
	checkout := func(r Result) Result { return r }
	results = append(results, sneaky())
	results = append(results, checkout(sneaky()))
	results = append(results, quiet())
	return results
}

func sneaky() Result { return helper() }

func helper() Result { githook.Find(); return Result{} }

func quiet() Result { return Result{} }
`)
	write("githook/githook.go", `package githook
import "github.com/punt-labs/ethos/v4/internal/resolve"

func Find() { _ = resolve.Roots() }
`)
	runAll := parseRunAll(t, filepath.Join(dir, "doctor", "doctor.go"))
	locating := locatingFuncs(t, map[string]string{
		"doctor": filepath.Join(dir, "doctor"), "githook": filepath.Join(dir, "githook"),
	})
	assert.True(t, locating["doctor.helper"])
	assert.True(t, locating["doctor.sneaky"])
	assert.False(t, locating["doctor.quiet"])
	_, ungated := gateAudit(runAll, locating)
	assert.Equal(t, []string{"sneaky"}, ungated)
}

type gateFunction struct {
	*ast.FuncDecl
	imports map[string]string // local import name → full path
}

// parseRunAll retains file import bindings as well as the runAll declaration.
func parseRunAll(t *testing.T, file string) gateFunction {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	require.NoError(t, err)
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "runAll" {
			return gateFunction{fn, gateImports(t, f)}
		}
	}
	t.Fatalf("no runAll in %s", file)
	return gateFunction{}
}

const gateInternalPath = "github.com/punt-labs/ethos/v4/internal/"

func gateImports(t *testing.T, f *ast.File) map[string]string {
	t.Helper()
	imports := map[string]string{}
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		require.NoError(t, err)
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		// These packages are inspected by this audit. A dot import needs new
		// binding analysis, not silently missing all calls through that import.
		if path == gateInternalPath+"resolve" || path == gateInternalPath+"githook" || path == "path/filepath" {
			require.NotEqual(t, ".", name, "gate audit does not support dot import of %s", path)
		}
		imports[name] = path
	}
	return imports
}

// importedCall binds a package selector by its import path, including renamed
// imports. The parser's local objects distinguish a shadowing local variable
// from the imported package. This does not resolve function-value aliases.
func importedCall(call *ast.CallExpr, imports map[string]string) (path, name string) {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	pkg, ok := ast.Unparen(sel.X).(*ast.Ident)
	if !ok || pkg.Obj != nil {
		return "", ""
	}
	return imports[pkg.Name], sel.Sel.Name
}

// These are the current exported repository locators, not a naming heuristic.
// ConfigOf reads an explicit store's config; IsWorkingTree classifies a given
// directory. Neither locates the request's repository. New locator APIs require
// extending this list and the negative fixtures.
func locatorCall(call *ast.CallExpr, imports map[string]string) bool {
	path, name := importedCall(call, imports)
	return path == gateInternalPath+"resolve" && (name == "Roots" || name == "CheckoutOf")
}

// locatingFuncs returns the functions and methods ("pkg.Name") in the
// non-test files of the packages named by dirs (package name → directory)
// that call a repository locator, directly or through other functions in
// those packages. Imports bind by path, not spelling. Methods are conservatively
// matched by name within the current package (receiver types are not resolved).
// All non-test files are scanned, including other platforms. Calls hidden by
// function values, interfaces or packages outside dirs are not followed.
func locatingFuncs(t *testing.T, dirs map[string]string) map[string]bool {
	t.Helper()
	locating := map[string]bool{}
	calls := map[string][]string{}
	for pkg, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		require.NotEmpty(t, files, dir)
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			require.NoError(t, err)
			imports := gateImports(t, f)
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				key := pkg + "." + fn.Name.Name
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if locatorCall(call, imports) {
						locating[key] = true
					} else if c := calleeKey(pkg, call, imports, dirs); c != "" {
						calls[key] = append(calls[key], c)
					}
					return true
				})
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for key, cs := range calls {
			if locating[key] {
				continue
			}
			for _, c := range cs {
				if locating[c] {
					locating[key], changed = true, true
					break
				}
			}
		}
	}
	return locating
}

// calleeKey names the function a call in package pkg invokes, as "pkg.Name",
// when it can be in one of dirs' packages; "" otherwise.
func calleeKey(pkg string, call *ast.CallExpr, imports, dirs map[string]string) string {
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		if f.Obj != nil {
			if _, isFunc := f.Obj.Decl.(*ast.FuncDecl); !isFunc {
				return "" // local function values are not package functions
			}
		}
		return pkg + "." + f.Name
	case *ast.SelectorExpr:
		if path, name := importedCall(call, imports); path != "" {
			for p := range dirs {
				if path == gateInternalPath+p {
					return p + "." + name
				}
			}
			return ""
		}
		return pkg + "." + f.Sel.Name // a method call, matched by name
	}
	return ""
}

// gateDerivation recognizes only conversions and the listed path derivations.
// Their results carry input provenance; unlike checks, they do not emit rows.
func gateDerivation(call *ast.CallExpr, imports map[string]string) bool {
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.ArrayType:
		return true // syntactic slice/array conversion
	case *ast.Ident:
		if f.Obj != nil {
			return f.Obj.Kind == ast.Typ
		}
		return f.Name == "string" // the predeclared conversion used for paths
	}
	path, name := importedCall(call, imports)
	if path == "path/filepath" {
		switch name {
		case "Join", "Clean", "Dir", "Abs", "EvalSymlinks":
			return true
		}
	}
	return false
}

// gateAudit follows runAll's repoRoot, storeRoot, s and teams parameters by
// parser object, so local shadowing does not inherit their provenance. Local
// assignments/declarations propagate monotonically through non-call expressions
// and gateDerivation calls; tuple results conservatively taint all targets.
// Other calls are judged separately, not assumed to return a root (a Result
// must not taint results/append). This is flow-insensitive and field-insensitive:
// it may over-report after reassignment or extraction from an aggregate. It
// does not follow heap writes, arbitrary helper return values, function aliases,
// or delayed closure execution. The top-level checkout/store locals are trusted
// gate bindings; the runtime tests verify their implementation. New forms need
// explicit audit/fixture changes, not a claim that this proves all future checks.
func gateAudit(runAll gateFunction, locating map[string]bool) (gated, ungated []string) {
	const kindCheckout, kindStore = 1, 2
	inputKinds := map[string]int{
		"repoRoot": kindCheckout, "storeRoot": kindStore, "s": kindStore, "teams": kindStore,
	}
	// Parser objects suffice for the local lexical bindings this syntax audit
	// follows. They deliberately do not stand in for type/receiver resolution.
	located := map[*ast.Object]int{} //nolint:staticcheck // SA1019: bounded local syntax analysis, not general object resolution.
	for _, field := range runAll.Type.Params.List {
		for _, id := range field.Names {
			located[id.Obj] = inputKinds[id.Name]
		}
	}
	gates := map[*ast.Object]int{} //nolint:staticcheck // SA1019: only the parser-local checkout/store bindings are trusted.
	for _, stmt := range runAll.Body.List {
		if as, ok := stmt.(*ast.AssignStmt); ok && as.Tok == token.DEFINE {
			for _, e := range as.Lhs {
				if id, ok := e.(*ast.Ident); ok {
					switch id.Name {
					case "checkout":
						gates[id.Obj] = kindCheckout
					case "store":
						gates[id.Obj] = kindStore
					}
				}
			}
		}
	}
	var kindOf func(ast.Expr) int
	kindOf = func(e ast.Expr) int {
		kind := 0
		ast.Inspect(e, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Obj != nil {
					kind |= located[x.Obj]
				}
			case *ast.FuncLit:
				return false
			case *ast.CallExpr:
				if gateDerivation(x, runAll.imports) {
					for _, a := range x.Args {
						kind |= kindOf(a)
					}
				}
				return false
			}
			return true
		})
		return kind
	}
	propagate := func(lhs, rhs []ast.Expr) bool {
		changed := false
		for i, l := range lhs {
			id, ok := l.(*ast.Ident)
			if !ok || id.Obj == nil || id.Name == "_" {
				continue
			}
			kind := 0
			if len(lhs) == len(rhs) {
				kind = kindOf(rhs[i])
			} else {
				for _, r := range rhs {
					kind |= kindOf(r)
				}
			}
			if located[id.Obj]|kind != located[id.Obj] {
				located[id.Obj] |= kind
				changed = true
			}
		}
		return changed
	}
	for changed := true; changed; {
		changed = false
		ast.Inspect(runAll.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				changed = propagate(x.Lhs, x.Rhs) || changed
			case *ast.ValueSpec:
				var lhs []ast.Expr
				for _, id := range x.Names {
					lhs = append(lhs, id)
				}
				changed = propagate(lhs, x.Values) || changed
			}
			return true
		})
	}

	var walk func(n ast.Node, inCheckout, inStore bool)
	walk = func(n ast.Node, inCheckout, inStore bool) {
		ast.Inspect(n, func(c ast.Node) bool {
			call, ok := c.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok && id.Obj != nil && gates[id.Obj] != 0 {
				for _, a := range call.Args {
					walk(a, inCheckout || gates[id.Obj] == kindCheckout, inStore || gates[id.Obj] == kindStore)
				}
				return false
			}
			name := types.ExprString(call.Fun)
			if gateDerivation(call, runAll.imports) {
				return true
			}
			if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok && (id.Name == "gateCheckout" || id.Name == "gateStore" || id.Name == "unlocated") {
				if id.Obj == nil {
					return true // package-level gate implementation, in another file
				}
				if _, isFunc := id.Obj.Decl.(*ast.FuncDecl); isFunc {
					return true
				}
			}
			if !inCheckout && !inStore && (locatorCall(call, runAll.imports) || locating[calleeKey("doctor", call, runAll.imports, map[string]string{"doctor": "", "githook": ""})]) {
				ungated = append(ungated, name)
				return true
			}
			kind := 0
			for _, a := range call.Args {
				kind |= kindOf(a)
			}
			if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
				kind |= kindOf(sel.X) // a method on a located store alias
			}
			if kind == 0 {
				return true
			}
			if (kind&kindCheckout != 0 && !inCheckout) || (kind&kindStore != 0 && !inStore) {
				ungated = append(ungated, name)
			} else {
				gated = append(gated, name)
			}
			return true
		})
	}
	walk(runAll.Body, false, false)
	return gated, ungated
}

// An unreadable store directory is not "not enabled": the hook presence check
// FAILs naming it, and a store that was not located turns a "not in a repo" row
// into a WARN that says it was not checked.
func TestHookPresenceUnreadableStoreIsNotDormant(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file modes are not POSIX permissions here, or root ignores them")
	}
	repo := repoTemp(t)
	store := filepath.Join(repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(store, 0o755))
	require.NoError(t, os.Chmod(store, 0o000))
	t.Cleanup(func() { _ = os.Chmod(store, 0o755) })

	r := CheckSealHook(repo)
	assert.Equal(t, "FAIL", r.Status, r.Detail)
	assert.Contains(t, r.Detail, "cannot determine enablement here")
	assert.NotContains(t, r.Detail, "not enabled")

	g := gateStore(Result{Name: "x", Status: "PASS", Detail: "not in a repo"}, "", errors.New("store broken"))
	assert.Equal(t, "WARN", g.Status)
	assert.Contains(t, g.Detail, "not checked")
	assert.NotContains(t, g.Detail, "not in a repo")
}

// gateStore's note on a FAIL is worded from the failure: under a policy issue the
// store was located, and the note says the configuration is unusable, not that
// the store was not located.
func TestGateStoreNoteFollowsTheFailure(t *testing.T) {
	policy := &resolve.ConfigError{Err: errors.New("unknown resolution"), What: "the repository's bundle or policy configuration is unusable", Policy: true}
	g := gateStore(Result{Name: "x", Status: "FAIL", Detail: "boom"}, "", policy)
	assert.Equal(t, "FAIL", g.Status)
	assert.Contains(t, g.Detail, "the repository's bundle or policy configuration is unusable: unknown resolution")
	assert.NotContains(t, g.Detail, "not located")

	g = gateStore(Result{Name: "x", Status: "FAIL", Detail: "boom"}, "", errors.New("git refused"))
	assert.Contains(t, g.Detail, "repo store not located: git refused")
}

// Under a policy issue rows that RunAll gates as store checks must keep the same
// behavior: a PASS becomes "not checked" with the issue's wording, a FAIL stays
// FAIL with the note. This comparison observes rows changed by the existing
// store gate; it does not independently discover an ungated store reader. The
// bounded source audit above supplies a separate tripwire for such omissions.
func TestRunAllForPolicyIssueGatesEveryStoreRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USER", "mal")
	repo := physicalAgentRoot(t, t.TempDir())
	ethosDir := filepath.Join(repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "identities"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), []byte("active_bundle: nosuchbundle\n"), 0o644))
	s, ss := identity.NewStore(ethosDir), session.NewStore(ethosDir)
	writeIdentity(t, ethosDir, "mal", "name: Mal\nhandle: mal\nkind: human\n")
	writeIdentity(t, ethosDir, "zoe", "name: Zoe\nhandle: zoe\nkind: human\n")

	unlocated := errors.New("store not located")
	base := RunAll(s, ss, repo, nil, repo, nil, nil)
	gated := RunAll(s, ss, repo, nil, "", unlocated, nil)
	byName := func(rs []Result) map[string]Result {
		m := map[string]Result{}
		for _, r := range rs {
			m[r.Name] = r
		}
		return m
	}
	baseRows := byName(base)

	repoConfig, err := resolve.ConfigOf(repo)
	require.NoError(t, err)
	loc := resolve.Located{Store: repo, Ethos: ethosDir, Checkout: repo, Config: repoConfig}
	got := byName(RunAllFor(loc, s, ss, nil))
	cfg, ok := got["Repository configuration"]
	require.True(t, ok, "the failing configuration row")
	assert.Equal(t, "FAIL", cfg.Status)

	storeRows := 0
	for _, g := range gated {
		b, ok := baseRows[g.Name]
		if !ok || (g.Detail == b.Detail && g.Status == b.Status) {
			continue // not a store row: the gate left it alone, or it is the gate's own row
		}
		storeRows++
		r := got[g.Name]
		if b.Status == "PASS" {
			assert.Equal(t, "WARN", r.Status, g.Name)
			assert.True(t, strings.HasPrefix(r.Detail, "not checked: the repository's bundle or policy configuration is unusable"), "%s: %s", g.Name, r.Detail)
			continue
		}
		assert.Equal(t, b.Status, r.Status, "%s keeps its status", g.Name)
		assert.Contains(t, r.Detail, "the repository's bundle or policy configuration is unusable", g.Name)
		assert.Contains(t, r.Detail, b.Detail, g.Name)
	}
	assert.GreaterOrEqual(t, storeRows, 6, "the comparison found too few store rows")
}

func TestCheckTeamMembers(t *testing.T) {
	// setup builds a repo whose team lists the given members, plus a global
	// store holding identities in global.
	setup := func(t *testing.T, config string, members, global []string) (string, *team.LayeredStore, identity.IdentityStore) {
		t.Helper()
		root, globalRoot := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o700))
		for _, h := range global {
			writeIdentity(t, globalRoot, h, "name: "+h+"\nhandle: "+h+"\nkind: agent\n")
		}
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
		if config != "" {
			require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte(config), 0o644))
		}
		ed := filepath.Join(root, ".punt-labs", "ethos")
		require.NoError(t, os.MkdirAll(filepath.Join(ed, "teams"), 0o755))
		y := "name: solo\nmembers:\n"
		for _, m := range members {
			y += "  - identity: " + m + "\n    role: r\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(ed, "teams", "solo.yaml"), []byte(y), 0o644))
		return root, team.NewLayeredStore(ed, ed),
			identity.NewLayeredStore(identity.NewStore(root), identity.NewStore(globalRoot))
	}

	t.Run("all resolve", func(t *testing.T) {
		root, teams, s := setup(t, "team: solo\n", []string{"a", "b"}, []string{"a", "b"})
		res := CheckTeamMembers(root, teams, s)
		assert.Equal(t, "PASS", res.Status, res.Detail)
		assert.Equal(t, "2 members resolve", res.Detail)
	})

	t.Run("unresolved members are all listed", func(t *testing.T) {
		root, teams, s := setup(t, "team: solo\n", []string{"a", "zed", "ghost"}, []string{"a"})
		res := CheckTeamMembers(root, teams, s)
		assert.Equal(t, "FAIL", res.Status)
		assert.Contains(t, res.Detail, "ghost, zed")
		assert.NotContains(t, res.Detail, ": a")
	})

	t.Run("no team configured", func(t *testing.T) {
		root, teams, s := setup(t, "", []string{"ghost"}, nil)
		res := CheckTeamMembers(root, teams, s)
		assert.Equal(t, "PASS", res.Status)
		assert.Equal(t, "no team configured", res.Detail)
	})

	t.Run("repo-only defers to the repo-set check", func(t *testing.T) {
		root, teams, s := setup(t, "team: solo\nresolution: repo-only\n", []string{"ghost"}, nil)
		res := CheckTeamMembers(root, teams, s)
		assert.Equal(t, "PASS", res.Status)
		assert.Contains(t, res.Detail, "Repo-only completeness")
	})
}
