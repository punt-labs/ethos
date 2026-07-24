//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Black-box CLI tests for harness-neutral sessions (DES-061). These
// exercise the built binary the way a Codex / plain-terminal user does,
// adding what the in-process unit tables and rsc's detached acceptance
// run do not commit: the full README journey as one subprocess flow,
// adversarial session IDs, concurrency, and lifecycle edges.
//
// Walk isolation. FindClaudePID walks the real process tree, and this
// test binary genuinely HAS a `claude` ancestor when run locally under
// Claude Code (verified: the go-test → ethos child chain reaches a
// `claude` process). The lead's "no claude ancestor" assumption does not
// hold for local runs. The walk is defeated a different, stronger way:
// every test uses a scratch HOME, and no current-pointer file is ever
// written there, so `ReadCurrentSession(FindClaudePID())` finds nothing
// regardless of ancestry — behaviorally identical to a detached ppid=1
// process. TestCLI_CodexJourney asserts this directly (step 0) and pins
// that `session start` mints a fresh 32-hex id rather than resurrecting a
// walked session.

var (
	sessionExportRe = regexp.MustCompile(`export ETHOS_SESSION=([0-9a-f]{32})`)
	agentExportRe   = regexp.MustCompile(`export ETHOS_AGENT_ID=(\S+)`)
)

// seededSessionEnv builds a scratch HOME + git repo, seeds starter
// content, and runs setup so a human identity (tester) and the claude
// agent exist. USER=tester so the standalone whoami chain resolves.
func seededSessionEnv(t *testing.T) *cliSubprocessEnv {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	gitInitDir(t, repo, home)
	se := &cliSubprocessEnv{
		home: home,
		repo: repo,
		env: []string{
			"HOME=" + home,
			"USER=tester",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"PATH=" + os.Getenv("PATH"),
		},
	}
	_, stderr, code := runCLI(t, se, "seed")
	require.Equal(t, 0, code, "seed: %s", stderr)
	answers := filepath.Join(repo, "answers.yaml")
	require.NoError(t, os.WriteFile(answers, []byte("name: Tester\nhandle: tester\nbundle: foundation\n"), 0o644))
	_, stderr, code = runCLI(t, se, "setup", "--file", answers, "--bundle", "foundation")
	require.Equal(t, 0, code, "setup: %s", stderr)
	return se
}

// withSession returns a copy of se's env with ETHOS_SESSION (and
// optionally ETHOS_AGENT_ID) appended — the shell state after
// eval "$(ethos session start ...)".
func withSession(se *cliSubprocessEnv, sessionID, agentID string) *cliSubprocessEnv {
	env := append([]string{}, se.env...)
	env = append(env, "ETHOS_SESSION="+sessionID)
	if agentID != "" {
		env = append(env, "ETHOS_AGENT_ID="+agentID)
	}
	return &cliSubprocessEnv{home: se.home, repo: se.repo, env: env}
}

// TestCLI_CodexJourney runs the README onboarding flow as one subprocess
// sequence in a detached-equivalent env: session start --persona, eval,
// iam, whoami, session, a mission claim attempt, and session end.
func TestCLI_CodexJourney(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededSessionEnv(t)

	// Step 0 — walk is defeated by HOME isolation: with no ETHOS_SESSION,
	// session show finds nothing even though a claude ancestor exists,
	// because the scratch HOME has no current-pointer for that PID.
	stdout, _, code := runCLI(t, se, "session", "show")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "No active session.",
		"detached-equivalent: no walked session resolves from a scratch HOME")

	// Step 1 — start a session with a declared persona. stdout is the
	// eval-able exports; assert a freshly minted 32-hex id (mint, not a
	// walked UUID) and the paired agent-id export.
	stdout, _, code = runCLI(t, se, "session", "start", "--persona", "claude")
	require.Equal(t, 0, code)
	m := sessionExportRe.FindStringSubmatch(stdout)
	require.NotNil(t, m, "session start must export a 32-hex ETHOS_SESSION; got %q", stdout)
	sessionID := m[1]
	am := agentExportRe.FindStringSubmatch(stdout)
	require.NotNil(t, am, "--persona must also export ETHOS_AGENT_ID; got %q", stdout)
	require.Equal(t, "claude", am[1])

	// Step 2 — the eval'd shell now carries both exports.
	sh := withSession(se, sessionID, "claude")

	// Step 3 — iam succeeds against the active session.
	_, stderr, code := runCLI(t, sh, "iam", "claude")
	require.Equal(t, 0, code, "iam should exit 0 under an active session; stderr=%s", stderr)

	// Step 4 — whoami reflects the declared persona (not the git/OS user).
	stdout, stderr, code = runCLI(t, sh, "whoami")
	require.Equal(t, 0, code, "whoami stderr=%s", stderr)
	assert.Contains(t, stdout, "claude", "whoami should report the session persona")

	// Step 5 — session (show) lists the roster for the active session.
	stdout, _, code = runCLI(t, sh, "session", "show")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "claude", "roster should include the primary persona")

	// Step 6 — mission claim takes the HARD chain: with a valid session it
	// gets PAST session resolution and fails at mission lookup, proving the
	// session resolved (not an errNoSession).
	_, stderr, code = runCLI(t, sh, "mission", "claim", "zzz-no-such-mission")
	require.NotEqual(t, 0, code, "claiming a bogus mission must fail")
	assert.NotContains(t, stderr, "no active session",
		"the session resolved; failure must be at mission lookup, not resolution")

	// Step 7 — session end deletes the roster.
	stdout, _, code = runCLI(t, sh, "session", "end")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "ended session "+sessionID)
	assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "sessions", sessionID+".yaml"))
}
