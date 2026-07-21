//go:build !windows

package mission

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionBoundMissions_ClaimSidecar binds a mission to a session through
// the `ethos mission claim` active-mission sidecar alone — no chunk, no
// delegation.
func TestSessionBoundMissions_ClaimSidecar(t *testing.T) {
	globalRoot := t.TempDir()
	repo := t.TempDir()
	require.NoError(t, WriteActiveMission(globalRoot, "sess1", "m-2026-07-21-001"))

	got, err := SessionBoundMissions(globalRoot, repo, "sess1")
	require.NoError(t, err)
	assert.Equal(t, []string{"m-2026-07-21-001"}, got)
}

// TestSessionBoundMissions_Delegation binds a mission to a session through a
// Tier B delegation record, matched on either the worker session or the parent
// session, and unions with the claim sidecar without duplicating.
func TestSessionBoundMissions_Delegation(t *testing.T) {
	globalRoot := t.TempDir()
	repo := t.TempDir()

	// Worker-session match.
	_, err := WriteDelegationSkeleton(repo, "m-2026-07-21-002", "d-2026-07-21-001",
		DelegationSkeleton{Tier: TierB, Session: "sess1", AgentType: "bwk"})
	require.NoError(t, err)
	// Parent-session match on a second mission.
	_, err = WriteDelegationSkeleton(repo, "m-2026-07-21-003", "d-2026-07-21-002",
		DelegationSkeleton{Tier: TierB, ParentSession: "sess1", AgentType: "rsc"})
	require.NoError(t, err)
	// A delegation for a different session must not bind.
	_, err = WriteDelegationSkeleton(repo, "m-2026-07-21-004", "d-2026-07-21-003",
		DelegationSkeleton{Tier: TierB, Session: "other", AgentType: "mdm"})
	require.NoError(t, err)
	// The claim sidecar points at m-...-002 too — must not double-count.
	require.NoError(t, WriteActiveMission(globalRoot, "sess1", "m-2026-07-21-002"))

	got, err := SessionBoundMissions(globalRoot, repo, "sess1")
	require.NoError(t, err)
	assert.Equal(t, []string{"m-2026-07-21-002", "m-2026-07-21-003"}, got)
}

// TestSessionBoundMissions_EmptySession short-circuits: an unknown session
// binds to nothing.
func TestSessionBoundMissions_EmptySession(t *testing.T) {
	got, err := SessionBoundMissions(t.TempDir(), t.TempDir(), "")
	require.NoError(t, err)
	assert.Nil(t, got)
}
