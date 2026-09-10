package doctor

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/githook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/seed"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/team"
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
// fresh temp root. The identities directory is created so CheckIdentityDir
// passes by default; individual tests remove it when they need a failure.
func newFixture(t *testing.T) (*identity.Store, *session.Store, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
	return identity.NewStore(root), session.NewStore(root), root
}

// outsideRepoTempDir creates a tempdir at /tmp so none of its ancestors
// contain a .git directory. Required for tests that must exercise the
// "not in a git repo" branch of FindRepoRoot — t.TempDir() honors
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

func TestCheckIdentityDir(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		s, ss, _ := newFixture(t)
		detail, ok := CheckIdentityDir(s, ss, false)
		assert.True(t, ok)
		assert.Equal(t, s.IdentitiesDir(), detail)
	})

	t.Run("missing", func(t *testing.T) {
		s, ss, root := newFixture(t)
		require.NoError(t, os.RemoveAll(filepath.Join(root, "identities")))
		detail, ok := CheckIdentityDir(s, ss, false)
		assert.False(t, ok)
		assert.Contains(t, detail, "not found")
	})

	// A layered repo whose repo-local identities dir is absent is healthy
	// ONLY when the repo is teams-only (hasRepoTeam) and the global store
	// holds identities: the fallback resolves to the global dir. With no
	// repo-local team the same absent dir must still FAIL, so an
	// uninitialized submodule checkout is not masked by a populated global
	// store.
	t.Run("layered teams-only falls back to global", func(t *testing.T) {
		repoRoot := t.TempDir()   // no identities/ subdir
		globalRoot := t.TempDir() // has identities/
		require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o700))
		ls := identity.NewLayeredStore(identity.NewStore(repoRoot), identity.NewStore(globalRoot))
		ss := session.NewStore(globalRoot)

		detail, ok := CheckIdentityDir(ls, ss, true)
		assert.True(t, ok, "teams-only repo should pass via the global fallback")
		assert.Equal(t, identity.NewStore(globalRoot).IdentitiesDir(), detail)
	})

	t.Run("layered no repo team still fails", func(t *testing.T) {
		repoRoot := t.TempDir()   // no identities/ subdir
		globalRoot := t.TempDir() // has identities/
		require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o700))
		ls := identity.NewLayeredStore(identity.NewStore(repoRoot), identity.NewStore(globalRoot))
		ss := session.NewStore(globalRoot)

		detail, ok := CheckIdentityDir(ls, ss, false)
		assert.False(t, ok, "no repo-local team: absent identities must FAIL, not fall back")
		assert.Contains(t, detail, "not found")
	})
}

func TestHasRepoLocalTeam(t *testing.T) {
	t.Run("empty storeRoot", func(t *testing.T) {
		assert.False(t, hasRepoLocalTeam(""))
	})

	t.Run("no team dir", func(t *testing.T) {
		assert.False(t, hasRepoLocalTeam(t.TempDir()))
	})

	t.Run("repo-local team present", func(t *testing.T) {
		storeRoot := t.TempDir()
		teamsDir := filepath.Join(storeRoot, ".punt-labs", "ethos", "teams")
		require.NoError(t, os.MkdirAll(teamsDir, 0o700))
		require.NoError(t, os.WriteFile(
			filepath.Join(teamsDir, "foundation.yaml"),
			[]byte("name: foundation\n"), 0o600))
		assert.True(t, hasRepoLocalTeam(storeRoot))
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
		r := CheckHumanIdentity(s, ss)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "mal")
	})

	t.Run("fresh install — no identities", func(t *testing.T) {
		// An empty store is the expected first-run state, not a fault: WARN
		// and point the user at `ethos setup`.
		s, ss, _ := newFixture(t)
		t.Setenv("USER", "ghost")
		r := CheckHumanIdentity(s, ss)
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
		r := CheckHumanIdentity(s, ss)
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
		r := CheckHumanIdentity(s, ss)
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
		r := CheckHumanIdentity(s, ss)
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
		dir := t.TempDir()
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
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	t.Run("dormant: no marker, foreign hook without ethos → PASS not enabled here", func(t *testing.T) {
		dir := t.TempDir()
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
		dir := t.TempDir()
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
	// point is that a dormant repo must PASS even when the sandbox
	// couldn't have run at all.
	t.Run("dormant: no git on PATH still PASSes — execution never attempted (S2/P1)", func(t *testing.T) {
		dir := t.TempDir()
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\ntrue\n"), 0o755))

		t.Setenv("PATH", t.TempDir()) // no git anywhere on PATH
		r := CheckSealHook(dir)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	// P5 superseded by qodo #5 (PR #515 review): P5 originally pinned PASS
	// here because before M1, only EXECUTION (hookInvocationObserved) could
	// see a standalone, unmarked call, and djb's review explicitly rejected
	// executing every foreign hook in every dormant repo just to tell PASS
	// from WARN. That reasoning was never about lexical detection — a
	// literal, textually-visible `ethos audit seal` needs no execution to
	// see. The dormant branch now also runs looksLikeInvocation (a pure
	// regex scan, same as the "gated-but-unenabled" marked-section case
	// already used), so this same fixture now WARNs. What P5's trade-off
	// still protects — and TestCheckSealHook's eval case right below pins
	// — is a call assembled dynamically (eval, a variable) that no lexical
	// scan, marker-based or pattern-based, can see without executing it.
	t.Run("dormant: a textually-literal unmarked standalone call now WARNs (qodo #5, supersedes P5)", func(t *testing.T) {
		dir := t.TempDir()
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		// No BEGIN/END markers — hasMarkerSection cannot see this call —
		// but it is unconditional and textually literal: a real commit in
		// this real (dormant) repo would actually invoke ethos, and the
		// call is visible to a lexical scan without running anything.
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))

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
		dir := t.TempDir()
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\ncmd='ethos audit seal'\neval \"$cmd\"\n"), 0o755))

		r := CheckSealHook(dir)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	t.Run("heredoc-quoted marker on a never-enabled repo → PASS not WARN", func(t *testing.T) {
		// A foreign hook that only documents the marker text inside a heredoc,
		// on a repo with no enabled marker, must not read as a chained section
		// (no false WARN) — the marker scan consults the heredoc mask (E2).
		dir := t.TempDir()
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		body := "#!/bin/sh\ncat <<'EOF'\n# --- BEGIN ETHOS DES-058 SEAL ---\nEOF\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o755))
		r := CheckSealHook(dir)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	t.Run("gated-but-unenabled: chained hook, no marker → WARN", func(t *testing.T) {
		dir := t.TempDir()
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"ethos audit seal || exit 2\n# --- END ETHOS DES-058 SEAL ---\n"
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o755))
		r := CheckSealHook(dir)
		assert.Equal(t, "WARN", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "not enabled here")
	})

	t.Run("enabled but no pre-commit hook → FAIL", func(t *testing.T) {
		dir := t.TempDir()
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
		dir := t.TempDir()
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
		orig := sandboxGOOS
		sandboxGOOS = "windows"
		t.Cleanup(func() { sandboxGOOS = orig })

		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"ethos audit seal || exit 2\n# --- END ETHOS DES-058 SEAL ---\n"
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
		orig := sandboxGOOS
		sandboxGOOS = "windows"
		t.Cleanup(func() { sandboxGOOS = orig })

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
		dir := writeEnabledHook(t, "#!/bin/sh\n# DES-058\nethos audit seal || exit 2\n")
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "standalone")
	})

	t.Run("chained seal section", func(t *testing.T) {
		body := "#!/bin/sh\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n" +
			"# --- BEGIN ETHOS DES-058 SEAL ---\nethos audit seal || exit 2\n" +
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
			"# if ! ethos audit seal; then exit 2; fi\n" +
			"# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "stale")
	})

	t.Run("mention in a foreign comment is not active", func(t *testing.T) {
		body := "#!/bin/sh\n# TODO: wire up ethos audit seal here\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not chained")
	})

	t.Run("inline trailing comment mention is not active", func(t *testing.T) {
		// The phrase in an inline comment after code must not read as a call.
		body := "#!/bin/sh\necho ok # ethos audit seal\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not chained")
	})

	t.Run("phrase as arguments to another command is not active", func(t *testing.T) {
		// `ethos audit seal` passed as args to echo is not a call (C1).
		body := "#!/bin/sh\necho ethos audit seal\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not chained")
	})

	t.Run("comment after a word-break char is not active", func(t *testing.T) {
		// Shell starts a comment after ';' or '&', not just whitespace (C2).
		for _, body := range []string{
			"#!/bin/sh\ntrue;# ethos audit seal\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n",
			"#!/bin/sh\ntrue &# ethos audit seal\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n",
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
		body := "#!/bin/sh\ntrue && ethos audit seal\n"
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
			"# --- BEGIN ETHOS DES-058 SEAL ---\nethos audit seal || exit 2\n# --- END ETHOS DES-058 SEAL ---\n"
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
		dir := t.TempDir()
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
		dir := writeEnabledHook(t, "#!/usr/bin/env python3\nethos audit seal\n")
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

		dir := t.TempDir()
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o644))
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

		dir := t.TempDir()
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		witness := filepath.Join(t.TempDir(), "witness")
		body := "#!/bin/sh\ntouch " + shQuote(witness) + "\nethos audit seal || exit 2\n"
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
				section:    "ethos audit seal || exit 2\n",
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
				orig := sandboxGOOS
				sandboxGOOS = "windows"
				t.Cleanup(func() { sandboxGOOS = orig })

				dir := t.TempDir()
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
			"# --- BEGIN ETHOS DES-058 SEAL ---\nethos audit seal || exit 2\n# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "chained")
	})

	t.Run("heredoc-quoted seal mention is not an active call", func(t *testing.T) {
		// A hook that only documents the seal in a heredoc body (usage/help
		// text) never runs it — CheckSealHook must NOT return PASS, or the
		// silent-absence bug this branch exists to close reopens.
		body := "#!/bin/sh\ncat <<'EOF'\nethos audit seal\nEOF\ntrue # stand-in for a foreign host hook's own logic — must not depend on any real external command being installed\n"
		dir := writeEnabledHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "not chained")
	})

	t.Run("gitdir file redirects hooks path", func(t *testing.T) {
		// A submodule .git file points directly at a dir with hooks/ and
		// no commondir — the hooks dir is target/hooks.
		real := t.TempDir()
		hooks := filepath.Join(real, "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))
		wt := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"),
			[]byte("gitdir: "+real+"\n"), 0o644))
		mark(t, wt)
		r := CheckSealHook(wt)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
	})

	t.Run("worktree resolves hooks through commondir", func(t *testing.T) {
		// Real worktree layout: the .git file points at
		// <main>/.git/worktrees/<name>, which has a commondir file (../..)
		// back to the main .git — where the hooks actually live. The seal
		// hook at the per-worktree admin dir must be ignored; only the
		// common hooks dir counts.
		mainGit := t.TempDir() // stands in for <main>/.git
		commonHooks := filepath.Join(mainGit, "hooks")
		require.NoError(t, os.MkdirAll(commonHooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(commonHooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))

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

		r := CheckSealHook(wt)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "standalone")
	})

	t.Run("worktree via real git worktree add", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		mainDir := t.TempDir()
		gitRun := func(dir string, args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
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
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))

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
		cmd := exec.Command("git", "-C", repo, "init", "-q")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git init: %s", out)
		cmd = exec.Command("git", "-C", repo, "config", "core.hooksPath", ".husky")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "git config: %s", out)

		// The seal lives where git runs hooks — the tracked .husky dir, the
		// same path the installer resolves via `git rev-parse --git-path hooks`.
		husky := filepath.Join(repo, ".husky")
		require.NoError(t, os.MkdirAll(husky, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(husky, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))
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
		cmd := exec.Command("git", "-C", repo, "init", "-q")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git init: %s", out)
		cmd = exec.Command("git", "-C", repo, "config", "core.hooksPath", ".husky")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "git config: %s", out)

		husky := filepath.Join(repo, ".husky")
		require.NoError(t, os.MkdirAll(husky, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(husky, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))
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
		dir := t.TempDir()
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
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
		r := CheckTrailerHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Equal(t, "not enabled here", r.Detail)
	})

	t.Run("enabled but no commit-msg hook → FAIL", func(t *testing.T) {
		dir := t.TempDir()
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
		body := "#!/bin/sh\n[ -z \"$1\" ] && exit 0\nethos hook commit-trailers\n"
		dir := writeEnabledHook(t, body)
		r := CheckTrailerHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "standalone")
	})

	t.Run("chained trailer section active", func(t *testing.T) {
		body := "#!/bin/sh\ntrue # stand-in for a foreign host hook's own logic\n" +
			"# --- BEGIN ETHOS DES-054 TRAILER ---\n" +
			"[ -z \"$1\" ] && exit 0\nethos hook commit-trailers\n" +
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
		dir := t.TempDir()
		hooksDir := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooksDir, 0o755))
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-054 TRAILER ---\n" +
			"[ -z \"$1\" ] && exit 0\nethos hook commit-trailers\n" +
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
		dir := t.TempDir()
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

	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755))
	// The seal hook is installed and healthy — only the trailer side is
	// missing, isolating the exact gap hy40 named.
	_, err := githook.Chain(filepath.Join(repo, ".git", "hooks", "pre-commit"), hooks.PreCommit, hooks.SealTag, hooks.SealIdent)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), nil, 0o644))

	results := RunAll(s, ss, repo, repo, nil)

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
	dir := t.TempDir()
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
		assert.Contains(t, r.Detail, "hand-duplicated")
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
	cmd := exec.Command("git", "-C", repo, "init", "-q")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init: %s", out)
	cmd = exec.Command("git", "-C", repo, "config", "core.hooksPath", ".husky")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
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

	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755))
	_, err := githook.Chain(filepath.Join(repo, ".git", "hooks", "pre-commit"), hooks.PreCommit, hooks.SealTag, hooks.SealIdent)
	require.NoError(t, err)
	_, err = githook.Chain(filepath.Join(repo, ".git", "hooks", "commit-msg"), hooks.CommitMsg, hooks.TrailerTag, hooks.TrailerIdent)
	require.NoError(t, err)
	// No enabled marker written — the repo stays dormant.

	results := RunAll(s, ss, repo, repo, nil)
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
	results := RunAll(s, ss, "", "", nil)
	require.Len(t, results, 14)

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
		"Audit seal hook",
		"Audit trailer hook",
		"Seal hook currency",
		"Trailer hook currency",
		"Repo-only completeness",
		"Local extension files",
		"Extension key names",
		"Mission file hand-edits",
		"Code archetype delegated-worker guard",
	}, names)

	assert.True(t, AllPassed(results), "results: %+v", results)
	assert.Equal(t, 14, PassedCount(results))

	// Now inject a failure: remove the identities directory. RunAll
	// should report at least one failure and AllPassed should flip.
	require.NoError(t, os.RemoveAll(filepath.Join(root, "identities")))
	results = RunAll(s, ss, "", "", nil)
	assert.False(t, AllPassed(results))
	assert.Less(t, PassedCount(results), 13)

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

	t.Run("with an identity store, stale and unresolved are distinguished", func(t *testing.T) {
		res := CheckOrphanedAgentFiles(root, root, teams, s)
		assert.Equal(t, "FAIL", res.Status)
		assert.Contains(t, res.Detail, "stale", "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "retired", "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "safe to delete", "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "unresolved", "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "phantom", "detail: %s", res.Detail)
		assert.Contains(t, res.Detail, "investigate", "detail: %s", res.Detail)
		if haveWounded {
			// N1: an unreadable-but-present identity file still classifies
			// as stale (a file IS there), never crashing into an error path
			// and never landing in the genuine-absence bucket.
			assert.Contains(t, res.Detail, "stale (known identity, not on the active team", "detail: %s", res.Detail)
			assert.Contains(t, res.Detail, "wounded", "detail: %s", res.Detail)
			assert.NotContains(t, res.Detail, "unresolved (no matching identity anywhere — investigate before deleting): wounded",
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
