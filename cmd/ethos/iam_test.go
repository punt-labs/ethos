package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
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
