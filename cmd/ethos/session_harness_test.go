//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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
	id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stdout), "export ETHOS_SESSION="))
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
	id := strings.TrimPrefix(lines[0], "export ETHOS_SESSION=")
	assert.Regexp(t, hex32, id)
	assert.Regexp(t, `^export ETHOS_SESSION=`, lines[0])

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
	assert.Contains(t, stdout, "export ETHOS_SESSION="+id, "start must report the existing id")

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
	assert.Contains(t, stdout, "export ETHOS_AGENT_ID=bwk")

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
	assert.Contains(t, stdout, "export ETHOS_AGENT_ID=test-agent",
		"--persona must emit the agent-id export; stdout=%q", stdout)

	var id string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, "export ETHOS_SESSION=") {
			id = strings.TrimPrefix(line, "export ETHOS_SESSION=")
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

// TestCLI_SessionEnd covers acceptance case 6: end deletes the roster, and a
// second end under the same env is a no-op (exit 0).
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

	// Second end is a safe no-op.
	_, stderr2, code2 := runCLI(t, env, "session", "end")
	assert.Equal(t, 0, code2, "second end must be a no-op; stderr=%s", stderr2)
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
