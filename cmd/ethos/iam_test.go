package main

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/process"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveSession_UnresolvableIsNamedErrorNotGitOSFallback pins the
// third DES-074 regression: a consumer that requires a session (iam,
// mission claim/release, all routed through resolveSession) must fail
// loud with a non-zero exit and a remedy — never silently succeed against
// some OTHER identity. resolveSession never consults git config or the OS
// user at all (that fallback chain lives one layer up, in resolve.Resolve,
// for consumers where a session is optional); this test pins that it
// still refuses cleanly with errNoSession when neither an explicit
// --session, ETHOS_SESSION, nor a Claude-process pointer resolves.
func TestResolveSession_UnresolvableIsNamedErrorNotGitOSFallback(t *testing.T) {
	missionTestEnv(t) // fresh HOME -> empty session store, no pointer files
	t.Setenv("ETHOS_SESSION", "")
	t.Setenv("ETHOS_AGENT_ID", "")
	// This suite usually runs inside a real Claude Code session, where
	// CLAUDE_PID is itself set; the fresh HOME above already guarantees no
	// pointer file exists for whatever PID resolves, but stripping it too
	// makes the walk path (rather than env+corroboration) the one
	// exercised here, matching this test's intent.
	t.Setenv("CLAUDE_PID", "")

	sessionID, agentID, err := resolveSession("", true)

	assert.Empty(t, sessionID)
	assert.Empty(t, agentID)
	assert.True(t, errors.Is(err, errNoSession), "must be the named, actionable errNoSession, not a generic failure")
	assert.Contains(t, err.Error(), "ethos session start", "the error must name the remedy")
}

// TestRunIam_UpdatesLegacyKeyedParticipant pins the round 2 finding at the
// CLI wiring level: `ethos iam` against a session created before DES-074
// (primary participant keyed on process.LegacyClaudePID, the walk-derived
// PID) must update that existing record, not file a second, duplicate
// participant under the new preferred process.FindClaudePID key.
func TestRunIam_UpdatesLegacyKeyedParticipant(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("ETHOS_AGENT_ID", "")
	// Force a live, corroborating CLAUDE_PID distinct from the walk
	// result: our GRANDPARENT, not our immediate parent, since in an
	// environment with no real "claude" ancestor (CI, a detached process)
	// the walk's own fallback is exactly os.Getppid() (see
	// resolve.TestResolve_TolerlatesLegacyKeyedParticipant for the same
	// trick and why it is needed).
	parentPID := os.Getppid()
	grandparentPID, err := process.ParentPID(parentPID)
	require.NoError(t, err, "need a real grandparent to run this test")
	t.Setenv("CLAUDE_PID", strconv.Itoa(grandparentPID))
	legacyPID := process.LegacyClaudePID()
	preferredPID := process.FindClaudePID()
	require.NotEqual(t, legacyPID, preferredPID,
		"test setup requires the legacy and preferred keys to differ")

	ss := session.NewStore(tmp + "/.punt-labs/ethos")
	require.NoError(t, ss.Create("legacy-iam-session",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: legacyPID, Persona: "old-persona"},
		"", "",
	))
	t.Setenv("ETHOS_SESSION", "legacy-iam-session")

	require.NoError(t, runIam("new-persona"))

	roster, err := ss.Load("legacy-iam-session")
	require.NoError(t, err)
	require.Len(t, roster.Participants, 2, "must update the existing legacy-keyed record, not append a duplicate")
	found := roster.FindParticipant(legacyPID)
	require.NotNil(t, found, "the on-disk key is left as-is; only the fields update")
	assert.Equal(t, "new-persona", found.Persona)
}

// TestRunHookCommitTrailers_SilentWhenNotUnderClaudeCode pins the ordinary
// case: a commit from a plain terminal or a non-Claude harness — no
// session was ever expected — emits no trailer AND no stderr noise. This
// hook fires on every commit, so warning here would make every ordinary
// commit noisy.
func TestRunHookCommitTrailers_SilentWhenNotUnderClaudeCode(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("ETHOS_SESSION", "")
	old := resolve.UnderClaudeCode
	resolve.UnderClaudeCode = func() bool { return false }
	t.Cleanup(func() { resolve.UnderClaudeCode = old })

	var out bytes.Buffer
	stderr := captureStderrFn(t, func() {
		require.NoError(t, runHookCommitTrailers(&out))
	})
	assert.Empty(t, out.String())
	assert.Empty(t, stderr)
}

// TestRunHookCommitTrailers_WarnsWhenUnresolvableUnderClaudeCode pins the
// round 2 (item 5) finding: a commit running under Claude Code whose
// session could not be identified must still emit no trailer and exit 0
// (this hook must never block a commit) but must say something on
// stderr — per its own rationale, a silently missing trailer is exactly
// the failure class it exists to prevent.
func TestRunHookCommitTrailers_WarnsWhenUnresolvableUnderClaudeCode(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("ETHOS_SESSION", "")
	old := resolve.UnderClaudeCode
	resolve.UnderClaudeCode = func() bool { return true }
	t.Cleanup(func() { resolve.UnderClaudeCode = old })

	var out bytes.Buffer
	stderr := captureStderrFn(t, func() {
		require.NoError(t, runHookCommitTrailers(&out))
	})
	assert.Empty(t, out.String(), "no trailer is emitted for an unresolvable session")
	assert.NotEmpty(t, stderr, "a session was expected but unresolvable; that must not go unnoticed")
	assert.NotContains(t, stderr, "ethos: ethos:")
}
