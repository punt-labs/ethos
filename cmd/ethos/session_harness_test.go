//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hex32 matches a minted session ID: exactly 32 lowercase hex characters.
var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// withEnv returns a copy of se with additional KEY=VALUE entries appended
// (later entries win in exec), leaving the original untouched.
func withEnv(se *cliSubprocessEnv, kv ...string) *cliSubprocessEnv {
	cp := *se
	cp.env = append(append([]string{}, se.env...), kv...)
	return &cp
}

// startedSessionID runs `session start` and returns the minted ID parsed
// from the export line.
func startedSessionID(t *testing.T, se *cliSubprocessEnv) string {
	t.Helper()
	stdout, stderr, code := runCLI(t, se, "session", "start")
	require.Equal(t, 0, code, "session start should exit 0; stderr=%s", stderr)
	id := strings.Trim(strings.TrimPrefix(strings.TrimSpace(stdout), "export ETHOS_SESSION="), "'")
	require.Regexp(t, hex32, id, "start must print a 32-hex export line; stdout=%q", stdout)
	return id
}

// TestCLI_SessionStart_MintsAndExports covers acceptance case 1: from a
// scratch repo with a clean HOME, `session start` succeeds, prints a single
// export line, and writes a roster whose ID is 32 hex characters.
func TestCLI_SessionStart_MintsAndExports(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)

	stdout, stderr, code := runCLI(t, se, "session", "start")
	require.Equal(t, 0, code, "stderr=%s", stderr)

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	require.Len(t, lines, 1, "stdout must be exactly the export line; got %q", stdout)
	id := strings.Trim(strings.TrimPrefix(lines[0], "export ETHOS_SESSION="), "'")
	assert.Regexp(t, hex32, id)
	assert.Regexp(t, `^export ETHOS_SESSION='[0-9a-f]{32}'$`, lines[0], "the export value must be single-quoted")

	rosterPath := filepath.Join(se.home, ".punt-labs", "ethos", "sessions", id+".yaml")
	assert.FileExists(t, rosterPath, "roster must exist for the minted id")
}

// TestCLI_SessionStart_ThenIam covers acceptance case 2: eval the export,
// then `iam` succeeds under it.
func TestCLI_SessionStart_ThenIam(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	id := startedSessionID(t, se)

	_, stderr, code := runCLI(t, withEnv(se, "ETHOS_SESSION="+id), "iam", "test-agent")
	assert.Equal(t, 0, code, "iam under ETHOS_SESSION should exit 0; stderr=%s", stderr)
}

// TestCLI_Iam_NoSession_Errors covers acceptance: without any session, iam
// errors naming `ethos session start`, not the old process-tree message.
func TestCLI_Iam_NoSession_Errors(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)

	_, stderr, code := runCLI(t, se, "iam", "test-agent")
	require.NotEqual(t, 0, code, "iam with no session must exit non-zero; stderr=%s", stderr)
	assert.Contains(t, stderr, "ethos session start", "error must name the remedy; stderr=%s", stderr)
	assert.NotContains(t, stderr, "process tree", "must not surface the old walker error; stderr=%s", stderr)
}

// TestCLI_SessionStart_Idempotent covers acceptance case 3: with a live
// ETHOS_SESSION, start reports it and mints no second roster.
func TestCLI_SessionStart_Idempotent(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	id := startedSessionID(t, se)

	stdout, stderr, code := runCLI(t, withEnv(se, "ETHOS_SESSION="+id), "session", "start")
	require.Equal(t, 0, code, "stderr=%s", stderr)
	assert.Contains(t, stdout, "export ETHOS_SESSION='"+id+"'", "start must report the existing id")

	// Exactly one roster exists.
	entries, err := os.ReadDir(filepath.Join(se.home, ".punt-labs", "ethos", "sessions"))
	require.NoError(t, err)
	var rosters int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			rosters++
		}
	}
	assert.Equal(t, 1, rosters, "idempotent start must not mint a second roster")
}

// TestSessionEnd_KeepsForeignPointer pins H1: ending a DIFFERENT session
// must not delete the caller's current-pointer. Run in-process so the test
// and handler share one FindClaudePID value.
func TestSessionEnd_KeepsForeignPointer(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)
	sessionEndSession = ""
	t.Cleanup(func() { sessionEndSession = "" })

	ss := sessionStore()
	root := session.Participant{AgentID: "jim", Persona: "jim"}
	require.NoError(t, ss.Create("s1live", root, session.Participant{AgentID: "a", Persona: "a", Parent: "jim"}, "", ""))
	require.NoError(t, ss.Create("s2end", root, session.Participant{AgentID: "b", Persona: "b", Parent: "jim"}, "", ""))

	pid := process.FindClaudePID()
	require.NoError(t, ss.WriteCurrentSession(pid, "s1live"))

	_, _, err := execHandler(t, "session", "end", "--session", "s2end")
	require.NoError(t, err)

	// The ended session is gone; the live session and its pointer survive.
	_, err = ss.Load("s2end")
	require.Error(t, err, "the named session must be deleted")
	_, err = ss.Load("s1live")
	require.NoError(t, err, "the live session's roster must survive")
	cur, err := ss.ReadCurrentSession(pid)
	require.NoError(t, err, "the live session's pointer must survive")
	assert.Equal(t, "s1live", cur)
}

// TestSessionEnd_WarnsOnUnverifiablePointer pins finding 8: when the
// current-pointer cannot be read (a non-not-found error), end leaves it in
// place — never delete on an unverifiable read — but surfaces the reason
// rather than swallowing it. In-process so the test and handler share one
// FindClaudePID.
func TestSessionEnd_WarnsOnUnverifiablePointer(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)
	sessionEndSession = ""
	t.Cleanup(func() { sessionEndSession = "" })

	ss := sessionStore()
	require.NoError(t, ss.Create("endme",
		session.Participant{AgentID: "jim", Persona: "jim"},
		session.Participant{AgentID: "a", Persona: "a", Parent: "jim"}, "", ""))
	t.Setenv("ETHOS_SESSION", "endme")

	// Make the current-pointer unreadable: a directory where the PID file
	// belongs forces EISDIR on read, uid-independent (chmod is bypassed by root).
	pid := process.FindClaudePID()
	pointer := filepath.Join(se.home, ".punt-labs", "ethos", "sessions", "current", pid)
	require.NoError(t, os.MkdirAll(pointer, 0o755))

	_, stderr, err := execHandler(t, "session", "end")
	require.NoError(t, err, "end must still succeed; stderr=%s", stderr)
	assert.Contains(t, stderr, "could not be verified", "an unverifiable pointer must be surfaced")

	info, statErr := os.Stat(pointer)
	require.NoError(t, statErr, "the pointer must be left in place")
	assert.True(t, info.IsDir())
}

// TestCLI_SessionStart_IdempotentPersonaJoins pins the gate defect: a
// re-run with --persona on the idempotent branch (live ETHOS_SESSION) must
// upsert-join the persona, not just advertise it — otherwise whoami keys on
// a participant that does not exist and falls back to git/OS. Re-running the
// same persona stays a single participant.
func TestCLI_SessionStart_IdempotentPersonaJoins(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	// A persona distinct from the roster's existing participants.
	idsDir := filepath.Join(se.home, ".punt-labs", "ethos", "identities")
	require.NoError(t, os.WriteFile(filepath.Join(idsDir, "bwk.yaml"),
		[]byte("name: Brian K\nhandle: bwk\nkind: agent\n"), 0o644))

	id := startedSessionID(t, se) // fresh start, no persona
	env := withEnv(se, "ETHOS_SESSION="+id)

	// Idempotent re-run WITH --persona must join bwk.
	stdout, stderr, code := runCLI(t, env, "session", "start", "--persona", "bwk")
	require.Equal(t, 0, code, "stderr=%s", stderr)
	assert.Contains(t, stdout, "export ETHOS_AGENT_ID='bwk'")

	// whoami under the eval'd env reflects bwk; USER=nobody so only the
	// session path can produce it.
	who, stderr, code := runCLI(t, withEnv(se, "ETHOS_SESSION="+id, "ETHOS_AGENT_ID=bwk", "USER=nobody"), "whoami")
	require.Equal(t, 0, code, "stderr=%s", stderr)
	assert.Contains(t, who, "bwk", "idempotent --persona must join bwk so whoami reflects it")

	// Re-running the same persona stays a single bwk participant.
	_, stderr, code = runCLI(t, env, "session", "start", "--persona", "bwk")
	require.Equal(t, 0, code, "stderr=%s", stderr)
	roster, err := session.NewStore(filepath.Join(se.home, ".punt-labs", "ethos")).Load(id)
	require.NoError(t, err)
	var bwkCount int
	for _, p := range roster.Participants {
		if p.AgentID == "bwk" {
			bwkCount++
		}
	}
	assert.Equal(t, 1, bwkCount, "re-running the same persona must not duplicate the participant")
}

// TestCLI_SessionStart_IdempotentPersonaParentIsRoot pins F2: a second
// re-run with ETHOS_AGENT_ID already exported must not self-parent the
// persona. Parent must be the root recorded at mint (read from the roster),
// so post-compaction primary discovery (Parent==root) still finds it.
func TestCLI_SessionStart_IdempotentPersonaParentIsRoot(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	id := startedSessionID(t, se) // fresh, no persona; root == test-agent

	// Two re-runs with the persona AND ETHOS_AGENT_ID exported — the second
	// is where a re-resolve would return the persona itself.
	env := withEnv(se, "ETHOS_SESSION="+id, "ETHOS_AGENT_ID=bwk")
	_, stderr, code := runCLI(t, env, "session", "start", "--persona", "bwk")
	require.Equal(t, 0, code, "stderr=%s", stderr)
	_, stderr, code = runCLI(t, env, "session", "start", "--persona", "bwk")
	require.Equal(t, 0, code, "stderr=%s", stderr)

	roster, err := session.NewStore(filepath.Join(se.home, ".punt-labs", "ethos")).Load(id)
	require.NoError(t, err)
	require.NotEmpty(t, roster.Participants)
	root := roster.Participants[0].AgentID

	var found bool
	for _, p := range roster.Participants {
		if p.AgentID == "bwk" {
			found = true
			assert.Equal(t, root, p.Parent, "persona must be parented to root, not itself")
			assert.NotEqual(t, "bwk", p.Parent, "must not self-parent")
		}
	}
	require.True(t, found, "persona bwk must be a participant")
}

// TestCLI_SessionEnd_CorruptRosterRefuses pins F3: end must refuse a corrupt
// roster (a crash artifact likely holding unsealed audit lines) rather than
// os.Remove it, mirroring start's M3 split.
func TestCLI_SessionEnd_CorruptRosterRefuses(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	sdir := filepath.Join(se.home, ".punt-labs", "ethos", "sessions")
	require.NoError(t, os.MkdirAll(sdir, 0o700))
	badID := "corruptccccccccccccccccccccccccc"
	rosterPath := filepath.Join(sdir, badID+".yaml")
	require.NoError(t, os.WriteFile(rosterPath, []byte("not a roster mapping\n"), 0o644))

	_, stderr, code := runCLI(t, withEnv(se, "ETHOS_SESSION="+badID), "session", "end")
	require.NotEqual(t, 0, code, "end over a corrupt roster must refuse; stderr=%s", stderr)
	assert.Contains(t, stderr, "unreadable roster")
	assert.FileExists(t, rosterPath, "end must not destroy the corrupt roster")
}

// TestCLI_SessionStart_CorruptRosterFails pins M3: ETHOS_SESSION naming an
// exists-but-unparseable roster must fail with a remedy rather than silently
// minting over it (repointing the env anchor away from a session whose
// unsealed audit lines just lost it). A not-found env still mints.
func TestCLI_SessionStart_CorruptRosterFails(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	sdir := filepath.Join(se.home, ".punt-labs", "ethos", "sessions")
	require.NoError(t, os.MkdirAll(sdir, 0o700))
	badID := "corruptbbbbbbbbbbbbbbbbbbbbbbbbbb"
	require.NoError(t, os.WriteFile(filepath.Join(sdir, badID+".yaml"), []byte("not a roster mapping\n"), 0o644))

	_, stderr, code := runCLI(t, withEnv(se, "ETHOS_SESSION="+badID), "session", "start")
	require.NotEqual(t, 0, code, "start over a corrupt roster must fail; stderr=%s", stderr)
	assert.Contains(t, stderr, "unreadable roster")

	entries, err := os.ReadDir(sdir)
	require.NoError(t, err)
	var rosters int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			rosters++
		}
	}
	assert.Equal(t, 1, rosters, "must not mint over the corrupt roster")

	// A not-found env is stale — start mints a fresh session (by design).
	stdout, stderr, code := runCLI(t, withEnv(se, "ETHOS_SESSION=doesnotexistdoesnotexistdoesnot"), "session", "start")
	require.Equal(t, 0, code, "not-found env must mint a fresh session; stderr=%s", stderr)
	newID := strings.Trim(strings.TrimPrefix(strings.TrimSpace(stdout), "export ETHOS_SESSION="), "'")
	assert.NotEqual(t, "doesnotexistdoesnotexistdoesnot", newID, "must not reuse the stale env id")
	assert.Regexp(t, hex32, newID)
}

// TestCLI_SessionStart_PersonaSelfConsistent pins REQ-1: with --persona,
// start emits a second `export ETHOS_AGENT_ID=<persona>` line and keys the
// primary on it, so the eval-then-whoami flow the README promises reflects
// the persona — even when $USER would resolve someone else.
func TestCLI_SessionStart_PersonaSelfConsistent(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)

	stdout, stderr, code := runCLI(t, se, "session", "start", "--persona", "test-agent")
	require.Equal(t, 0, code, "stderr=%s", stderr)
	assert.Contains(t, stdout, "export ETHOS_AGENT_ID='test-agent'",
		"--persona must emit the agent-id export; stdout=%q", stdout)

	var id string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, "export ETHOS_SESSION=") {
			id = strings.Trim(strings.TrimPrefix(line, "export ETHOS_SESSION="), "'")
		}
	}
	require.Regexp(t, hex32, id)

	// eval would export both; simulate that and confirm whoami reflects the
	// persona with $USER pointed at a non-identity so only the session path
	// can produce it.
	env := withEnv(se, "ETHOS_SESSION="+id, "ETHOS_AGENT_ID=test-agent", "USER=nobody")
	out, stderr, code := runCLI(t, env, "whoami")
	require.Equal(t, 0, code, "whoami should exit 0; stderr=%s", stderr)
	assert.Contains(t, out, "test-agent", "eval-then-whoami must reflect the --persona identity")
}

// TestCLI_SessionStart_RejectsUnsafePersona pins B1: a --persona with a
// space, quote, or shell metacharacter is rejected up front with an
// actionable error and never emitted into stdout (so eval cannot inject).
func TestCLI_SessionStart_RejectsUnsafePersona(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)

	for _, bad := range []string{"a b", "a;rm -rf /", "a'b", "a$(id)", "UPPER"} {
		stdout, stderr, code := runCLI(t, se, "session", "start", "--persona", bad)
		require.NotEqual(t, 0, code, "unsafe persona %q must be rejected; stdout=%q", bad, stdout)
		assert.Contains(t, stderr, "valid handle", "the rejection must be actionable")
		assert.NotContains(t, stdout, "export ETHOS_AGENT_ID", "an unsafe persona must never reach stdout")
	}
}

// TestCLI_SessionStart_RejectsNonHexReattach pins the Copilot hardening: an
// ETHOS_SESSION that names a loadable roster but is NOT the minted 32-hex
// shape (e.g. carrying a newline/control char) is refused — never echoed,
// no roster mutation — so it cannot become a multi-line eval surface.
func TestCLI_SessionStart_RejectsNonHexReattach(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	sdir := filepath.Join(se.home, ".punt-labs", "ethos", "sessions")
	require.NoError(t, os.MkdirAll(sdir, 0o700))

	// A loadable roster whose id carries a newline (rosterPath uses
	// filepath.Base, so a newline-bearing name resolves to a real file).
	badID := "abc\necho pwned"
	roster := "session: \"" + badID + "\"\nparticipants:\n  - agent_id: jim\n    persona: jim\n"
	require.NoError(t, os.WriteFile(filepath.Join(sdir, badID+".yaml"), []byte(roster), 0o644))

	stdout, stderr, code := runCLI(t, withEnv(se, "ETHOS_SESSION="+badID), "session", "start")
	require.NotEqual(t, 0, code, "a non-hex loadable ETHOS_SESSION must be refused; stdout=%q", stdout)
	assert.Contains(t, stderr, "32-hex")
	assert.NotContains(t, stdout, "export ETHOS_SESSION", "the bad id must never be echoed")
	assert.NotContains(t, stdout, "pwned")
}

// TestCLI_SessionStart_ExportsEvalCleanly pins B1: the export lines survive
// an actual `eval` in a POSIX shell and set the variables intact.
func TestCLI_SessionStart_ExportsEvalCleanly(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)

	stdout, stderr, code := runCLI(t, se, "session", "start", "--persona", "test-agent")
	require.Equal(t, 0, code, "stderr=%s", stderr)

	// Round-trip the exports through a real shell eval.
	script := stdout + "\nprintf '%s|%s\\n' \"$ETHOS_SESSION\" \"$ETHOS_AGENT_ID\"\n"
	shOut, err := exec.Command("sh", "-c", script).Output()
	require.NoError(t, err, "the export lines must eval cleanly")
	parts := strings.SplitN(strings.TrimSpace(string(shOut)), "|", 2)
	require.Len(t, parts, 2)
	assert.Regexp(t, hex32, parts[0], "ETHOS_SESSION must round-trip intact")
	assert.Equal(t, "test-agent", parts[1], "ETHOS_AGENT_ID must round-trip intact")
}

// TestCLI_SessionEnd: end deletes the roster; a second end under the same
// (now-stale) env is a clean no-op with a note — teardown is idempotent
// (rm -f), so H2's hard env verification does not apply here.
func TestCLI_SessionEnd(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	id := startedSessionID(t, se)
	rosterPath := filepath.Join(se.home, ".punt-labs", "ethos", "sessions", id+".yaml")
	require.FileExists(t, rosterPath)

	env := withEnv(se, "ETHOS_SESSION="+id)
	_, stderr, code := runCLI(t, env, "session", "end")
	require.Equal(t, 0, code, "stderr=%s", stderr)
	assert.NoFileExists(t, rosterPath, "end must delete the roster")

	// The env now names a deleted roster: a second end is a no-op with a note.
	_, stderr2, code2 := runCLI(t, env, "session", "end")
	assert.Equal(t, 0, code2, "second end must be a clean no-op; stderr=%s", stderr2)
	assert.Contains(t, stderr2, "nothing to end")
}

// TestCLI_Whoami_ReflectsPersona covers acceptance case 5 (REC-1/REC-3):
// after iam under a stable ETHOS_AGENT_ID, whoami reports the session
// persona even when $USER would not resolve — proving it read the session,
// not the OS fallback. A stable agent key is used because each subprocess
// gets a distinct PID, so the participant must be keyed on ETHOS_AGENT_ID
// for iam and whoami to agree (the exact REC-3 parity path).
func TestCLI_Whoami_ReflectsPersona(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	id := startedSessionID(t, se)

	// USER=nobody has no identity, so a successful whoami can only come
	// from the session persona, not the $USER fallback.
	env := withEnv(se, "ETHOS_SESSION="+id, "ETHOS_AGENT_ID=agent-key", "USER=nobody")
	_, stderr, code := runCLI(t, env, "iam", "test-agent")
	require.Equal(t, 0, code, "iam should exit 0; stderr=%s", stderr)

	stdout, stderr, code := runCLI(t, env, "whoami")
	require.Equal(t, 0, code, "whoami should exit 0; stderr=%s", stderr)
	assert.Contains(t, stdout, "test-agent", "whoami must reflect the session persona under ETHOS_AGENT_ID")

	// Standalone virtue preserved: with no session, whoami falls back to
	// $USER → handle (test-agent).
	stdout, stderr, code = runCLI(t, se, "whoami")
	require.Equal(t, 0, code, "whoami with no session should exit 0; stderr=%s", stderr)
	assert.Contains(t, stdout, "test-agent", "no-session whoami must use the git/OS fallback")
}

// TestCLI_MissionClaim_StaleEnvErrors pins H2: a stale ETHOS_SESSION must
// not sail through the hard chain and stage a phantom binding — claim
// refuses loudly, naming ETHOS_SESSION as the source.
func TestCLI_MissionClaim_StaleEnvErrors(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	env := withEnv(se, "ETHOS_SESSION=deadbeefdeadbeefdeadbeefdeadbeef")

	_, stderr, code := runCLI(t, env, "mission", "claim", "m-2026-05-23-001")
	require.NotEqual(t, 0, code, "claim under a stale session must refuse; stderr=%s", stderr)
	assert.Contains(t, stderr, "ETHOS_SESSION", "the refusal must name the stale source")
}

// TestCLI_Whoami_WarnsOnCorruptRoster pins M6: an ETHOS_SESSION that names
// an existing-but-unparseable roster warns on stderr rather than silently
// answering with the git/OS identity; whoami still falls back so it does
// not brick.
func TestCLI_Whoami_WarnsOnCorruptRoster(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	badID := "corruptaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rosterPath := filepath.Join(se.home, ".punt-labs", "ethos", "sessions", badID+".yaml")
	require.NoError(t, os.WriteFile(rosterPath, []byte("not a roster mapping\n"), 0o644))

	out, stderr, code := runCLI(t, withEnv(se, "ETHOS_SESSION="+badID), "whoami")
	require.Equal(t, 0, code, "whoami must fall back, not brick; stderr=%s", stderr)
	assert.Contains(t, stderr, "unreadable roster", "a corrupt named roster must warn")
	assert.Contains(t, out, "test-agent", "whoami falls back to the git/OS identity")
}

// TestCurrentSessionIDBestEffort_EnvVerification pins M5: a non-empty
// ETHOS_SESSION that names no loadable roster falls back to "" (the legacy
// tracked-log append) so a dead ID never enters the audit namespace; a live
// session's ID passes through.
func TestCurrentSessionIDBestEffort_EnvVerification(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	t.Setenv("ETHOS_SESSION", "deadbeefdeadbeefdeadbeefdeadbeef")
	assert.Empty(t, currentSessionIDBestEffort(), "a dead env id must fall back to the tracked log")

	ss := sessionStore()
	require.NoError(t, ss.Create("liveaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		session.Participant{AgentID: "jim", Persona: "jim"},
		session.Participant{AgentID: "a", Persona: "a", Parent: "jim"}, "", ""))
	t.Setenv("ETHOS_SESSION", "liveaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	assert.Equal(t, "liveaaaaaaaaaaaaaaaaaaaaaaaaaaaa", currentSessionIDBestEffort(),
		"a live env id passes through")
}
