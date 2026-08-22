//go:build !windows

package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// TestHandleDashboard_RendersAbandonedPill asserts an abandoned
// mission row renders with the pill-abandoned CSS class, not an
// unstyled bare pill. The status → CSS class mapping is dynamic
// (`pill-{{.Status}}` in dashboard.html), so the only way this class
// could be missing is if layout.html never defines `.pill-abandoned` —
// exactly the enumeration gap djb's round-2 review caught.
func TestHandleDashboard_RendersAbandonedPill(t *testing.T) {
	storeRoot := t.TempDir()
	globalRoot := t.TempDir()

	// readMissionsJSONL reads <storeRoot>/.punt-labs/ethos/missions.jsonl
	// directly, so the fixture writes one abandoned-status line rather
	// than driving a full Store.Abandon call — the handler test's
	// concern is rendering, not the transition itself (that's covered
	// in internal/mission).
	jsonlPath := mission.RepoStatePath(storeRoot, "missions.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(jsonlPath), 0o755))
	line := `{"id":"m-2026-08-06-090","status":"abandoned","leader":"claude","worker":"bwk","evaluator":"djb","created_at":"2026-08-06T00:00:00Z","closed_at":"2026-08-06T00:01:00Z"}` + "\n"
	require.NoError(t, os.WriteFile(jsonlPath, []byte(line), 0o644))

	srv, err := NewServer(storeRoot, storeRoot)
	require.NoError(t, err)
	srv.globalRoot = globalRoot

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.handleDashboard(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `pill-abandoned`, "abandoned mission row must render with the pill-abandoned class, got: %s", body)
	assert.Contains(t, body, `>abandoned<`, "the status text itself must render")
}

// TestLayoutCSS_DefinesAbandonedPill pins the CSS rule directly, so a
// future edit to layout.html that removes .pill-abandoned fails here
// even if a dashboard fixture happens not to exercise it.
func TestLayoutCSS_DefinesAbandonedPill(t *testing.T) {
	data, err := templateFS.ReadFile("templates/layout.html")
	require.NoError(t, err)
	assert.Contains(t, string(data), ".pill-abandoned",
		"layout.html must define a .pill-abandoned CSS rule, matching every other terminal status")
}

// TestHandleMission_RendersCorrections asserts the DES-072 rendering
// requirement on the UI surface: a correction filed against a closed
// mission appears in the mission detail page's Corrections section.
func TestHandleMission_RendersCorrections(t *testing.T) {
	storeRoot := t.TempDir()
	globalRoot := t.TempDir()

	store := mission.NewStoreWithRoots(storeRoot, globalRoot).
		WithCheckoutRoot(storeRoot).
		WithSessionID("s1")
	id := "m-2026-08-22-090"
	require.NoError(t, store.Create(uiTestContract(id)))
	require.NoError(t, store.AppendResult(id, &mission.Result{
		Mission:    id,
		Round:      1,
		Author:     "bwk",
		Verdict:    mission.VerdictPass,
		Confidence: 0.9,
		Evidence:   []mission.EvidenceCheck{{Name: "make check", Status: mission.EvidenceStatusPass}},
	}))
	_, err := store.Close(id, mission.StatusClosed)
	require.NoError(t, err)
	require.NoError(t, store.Correct(id, mission.Correction{
		Mission:   id,
		Kind:      mission.CorrectionFabrication,
		Author:    "claude",
		Claim:     "make check (full suite): fail — pre-existing, unrelated",
		Corrected: "make check failed because of a stale worktree base",
	}))

	srv, err := NewServer(storeRoot, storeRoot)
	require.NoError(t, err)
	srv.globalRoot = globalRoot
	srv.repoRoot = storeRoot

	req := httptest.NewRequest(http.MethodGet, "/missions/"+id, nil)
	rec := httptest.NewRecorder()
	srv.handleMission(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Corrections", "detail: %s", body)
	assert.Contains(t, body, "fabrication")
	assert.Contains(t, body, "make check failed because of a stale worktree base")
	assert.Contains(t, body, "by claude")
}

// uiTestContract returns a minimal valid open contract for id.
func uiTestContract(id string) *mission.Contract {
	return &mission.Contract{
		MissionID:       id,
		Status:          mission.StatusOpen,
		CreatedAt:       "2026-07-24T00:00:00Z",
		UpdatedAt:       "2026-07-24T00:00:00Z",
		Leader:          "claude",
		Worker:          "bwk",
		Evaluator:       mission.Evaluator{Handle: "djb", PinnedAt: "2026-07-24T00:00:00Z"},
		Inputs:          mission.Inputs{Ticket: "ethos-ui"},
		WriteSet:        []string{"internal/ui/"},
		Tools:           []string{"Read", "Write", "Edit"},
		SuccessCriteria: []string{"make check passes"},
		Budget:          mission.Budget{Rounds: 1, ReflectionAfterEach: true},
		CurrentRound:    1,
	}
}
