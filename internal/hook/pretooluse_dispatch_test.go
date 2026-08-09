package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stageMissionInStatus stages a contract and drives it to the given
// terminal status via the real store lifecycle: StatusClosed,
// StatusFailed, and StatusEscalated go through Store.Close (which is
// result-gated, so a passing result is submitted first); StatusAbandoned
// goes through Store.Abandon (which is delegation- and result-gated,
// so it must run before either exists). repo is the repo root
// Abandon's delegations-gate checks under — callers pass
// stageRepoRoot(t)'s return value so the store resolves the same
// repo tree the dispatch hook will.
func stageMissionInStatus(t *testing.T, home, repo, missionID, status string) {
	t.Helper()
	stageContract(t, home, missionID)
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	store := mission.NewStoreWithRoots(repo, globalRoot)
	if status == mission.StatusAbandoned {
		_, err := store.Abandon(missionID, "test: driving mission to abandoned for case-1 status re-check coverage")
		require.NoError(t, err)
		return
	}
	require.NoError(t, store.AppendResult(missionID, &mission.Result{
		Mission:    missionID,
		Round:      1,
		Author:     "bwk",
		Verdict:    mission.VerdictPass,
		Confidence: 0.9,
		Evidence:   []mission.EvidenceCheck{{Name: "make check", Status: mission.EvidenceStatusPass}},
	}))
	_, err := store.Close(missionID, status)
	require.NoError(t, err)
}

// TestDispatchAgent_EnvMissionIDStatusRecheck pins facet 2 of
// docs/design-delegation-lifecycle.md: case 1 (the MISSION_ID env
// var) must re-check the mission's status exactly like case 2's
// staleBindingReason already does. A MISSION_ID naming a mission that
// has left StatusOpen — closed, failed, escalated, or abandoned —
// must fall through to Tier A (spawn allowed, no MISSION_ID echoed)
// with the same shape of stderr warning readActiveMissionForDispatch
// already emits for a stale sidecar, never a blocked spawn. This is
// the fix for d-043/d-044: a MISSION_ID env value inherited by a
// resumed subagent process outlives the mission it named.
func TestDispatchAgent_EnvMissionIDStatusRecheck(t *testing.T) {
	statuses := []string{
		mission.StatusClosed,
		mission.StatusFailed,
		mission.StatusEscalated,
		mission.StatusAbandoned,
	}
	for i, status := range statuses {
		t.Run(status, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			repo := stageRepoRoot(t)

			missionID := fmt.Sprintf("m-2026-08-09-%03d", 710+i)
			stageMissionInStatus(t, home, repo, missionID, status)

			t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
			t.Setenv("MISSION_ID", missionID)
			t.Setenv("PARENT_DELEGATION_ID", "")
			t.Setenv("ETHOS_QUIET_ADVICE", "1")
			t.Setenv("PARENT_SESSION_ID", "")

			payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-` + status + `"}`
			var out bytes.Buffer
			warning := captureHookStderr(t, func() {
				require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
			})

			var r PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &r))
			assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
				"a MISSION_ID naming a %s mission must not block the spawn", status)
			assert.Empty(t, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
				"a %s mission must not take a new delegation", status)
			assert.NotEmpty(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"],
				"fall-through to Tier A must still allocate a DELEGATION_ID")
			assert.Contains(t, warning, missionID, "the warning must name the stale mission")
			assert.Contains(t, warning, status, "the warning must name the mission's current status")
		})
	}
}

// TestDispatchAgent_EnvMissionIDStatusRecheck_TOCTOU pins the second
// half of facet 2's status re-check: dispatchTierB re-Loads the
// contract immediately before WriteDelegationSkeleton, while holding
// AcquireMissionLock (shared) and AcquireDelegationLock (exclusive).
// This closes the window between the first Load (which saw the
// mission open) and the write: a concurrent Close whose contract
// transition commits while dispatch is still blocked acquiring its
// own shared mission lock must be observed by the second Load, and
// the spawn must fall through to Tier A rather than write a
// delegation skeleton under a mission that is now closed.
//
// The test holds AcquireMissionLockExclusive itself to force
// dispatch's shared-lock acquisition to block after its first Load
// succeeds — the same technique
// TestHandlePreToolUse_TierBDispatch_ExclusiveBlocks uses, extended
// with a concurrent Close whose contract write must land before the
// hold is released. Close's own contract transition uses a lock
// independent of AcquireMissionLock (s.lockPath, not the per-mission
// .lock file), so it commits even while this test holds the
// exclusive mission lock; only Close's delegation-sweep half blocks
// behind it.
func TestDispatchAgent_EnvMissionIDStatusRecheck_TOCTOU(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	missionID := "m-2026-08-09-720"
	stageContract(t, home, missionID)
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	store := mission.NewStoreWithRoots(repo, globalRoot)
	require.NoError(t, store.AppendResult(missionID, &mission.Result{
		Mission:    missionID,
		Round:      1,
		Author:     "bwk",
		Verdict:    mission.VerdictPass,
		Confidence: 0.9,
		Evidence:   []mission.EvidenceCheck{{Name: "make check", Status: mission.EvidenceStatusPass}},
	}))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("ETHOS_QUIET_ADVICE", "1")
	t.Setenv("PARENT_SESSION_ID", "")

	// Holding this also creates the repo-tree per-mission directory
	// Close's own exclusive-lock gate checks for before it bothers
	// locking (TestStore_TwoRoot_CloseStaysInItsLayer).
	releaseExcl, err := mission.AcquireMissionLockExclusive(repo, missionID)
	require.NoError(t, err)

	var out bytes.Buffer
	var dispatchErr error
	warning := captureHookStderr(t, func() {
		dispatchDone := make(chan struct{})
		go func() {
			payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-toctou"}`
			dispatchErr = HandlePreToolUse(strings.NewReader(payload), &out)
			close(dispatchDone)
		}()

		// Give the dispatch goroutine time to pass its first Load and
		// block on AcquireMissionLock (shared).
		time.Sleep(50 * time.Millisecond)

		closeDone := make(chan error, 1)
		go func() {
			_, closeErr := store.Close(missionID, mission.StatusClosed)
			closeDone <- closeErr
		}()

		// Give Close's contract transition time to commit before the
		// exclusive hold is released.
		time.Sleep(50 * time.Millisecond)
		releaseExcl()

		select {
		case closeErr := <-closeDone:
			require.NoError(t, closeErr)
		case <-time.After(5 * time.Second):
			t.Fatal("Close did not complete within 5s")
		}
		select {
		case <-dispatchDone:
		case <-time.After(5 * time.Second):
			t.Fatal("dispatch did not complete within 5s")
		}
	})
	require.NoError(t, dispatchErr)

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
		"a mission that closed while dispatch was blocked must not refuse the spawn")
	assert.Empty(t, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"the TOCTOU re-check must catch the close and fall through to Tier A")
	assert.Contains(t, warning, missionID)
	assert.Contains(t, warning, "closed")

	delegationsDir := filepath.Join(repo, ".punt-labs", "ethos", "missions", missionID, "delegations")
	entries, err := os.ReadDir(delegationsDir)
	if err == nil {
		assert.Empty(t, entries,
			"the TOCTOU fallback must fire before WriteDelegationSkeleton — no record should land under the closed mission")
	} else {
		assert.True(t, os.IsNotExist(err), "unexpected error reading delegations dir: %v", err)
	}
}
