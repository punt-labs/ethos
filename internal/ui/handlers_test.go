//go:build !windows

package ui

import (
	"testing"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServerMissionStore_ReadsEventsFromCheckoutRoot pins the Bugbot HIGH
// #370 class one layer up: the UI's mission store must read the DES-058 event
// union from the per-checkout root (the work tree), not the store root, or the
// mission timeline is empty when `ethos ui` runs from a linked worktree. The
// Server's repoRoot is the work-tree root (CR#2), so it is the checkout root
// for the event read.
func TestServerMissionStore_ReadsEventsFromCheckoutRoot(t *testing.T) {
	storeRoot := t.TempDir()    // shared record tree (main, in a worktree)
	checkoutRoot := t.TempDir() // the work tree the UI runs from
	globalRoot := t.TempDir()

	// Write a mission the DES-058 way: the contract lands under the store
	// root, its create event under the checkout root's live zone.
	writer := mission.NewStoreWithRoots(storeRoot, globalRoot).
		WithCheckoutRoot(checkoutRoot).
		WithSessionID("s1")
	id := "m-2026-07-24-080"
	require.NoError(t, writer.Create(uiTestContract(id)))

	// A Server whose work-tree root is the checkout reads a non-empty
	// timeline — the fix wires WithCheckoutRoot(s.repoRoot).
	srv := &Server{storeRoot: storeRoot, repoRoot: checkoutRoot, globalRoot: globalRoot}
	events, _, err := srv.missionStore().LoadEvents(id)
	require.NoError(t, err)
	assert.NotEmpty(t, events, "the mission timeline must be non-empty from a worktree")
	assert.Equal(t, "create", events[0].Event)

	// Regression guard: without the checkout root the audit union falls back
	// to the store root and the timeline is empty — the bug this fixes.
	buggy := mission.NewStoreWithRoots(storeRoot, globalRoot)
	bugEvents, _, err := buggy.LoadEvents(id)
	require.NoError(t, err)
	assert.Empty(t, bugEvents,
		"without the checkout root the UI would show an empty timeline from a worktree")
}

// uiTestContract returns a minimal valid open contract for id.
func uiTestContract(id string) *mission.Contract {
	return &mission.Contract{
		MissionID: id,
		Status:    mission.StatusOpen,
		CreatedAt: "2026-07-24T00:00:00Z",
		UpdatedAt: "2026-07-24T00:00:00Z",
		Leader:    "claude",
		Worker:    "bwk",
		Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: "2026-07-24T00:00:00Z"},
		Inputs:    mission.Inputs{Ticket: "ethos-ui"},
		WriteSet:  []string{"internal/ui/"},
		Tools:     []string{"Read", "Write", "Edit"},
		SuccessCriteria: []string{"make check passes"},
		Budget:          mission.Budget{Rounds: 1, ReflectionAfterEach: true},
		CurrentRound:    1,
	}
}
