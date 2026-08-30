//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_Abandon_SucceedsOnZeroDelegationMission asserts the
// baseline success path: a mission created but never dispatched to a
// worker — zero delegation records, zero results — abandons cleanly.
func TestStore_Abandon_SucceedsOnZeroDelegationMission(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract("m-2026-08-06-001")
	require.NoError(t, s.Create(c))

	abandoned, err := s.Abandon("m-2026-08-06-001", "dead mission, never dispatched")
	require.NoError(t, err)
	require.NotNil(t, abandoned)
	assert.Equal(t, StatusAbandoned, abandoned.Status)
	assert.NotEmpty(t, abandoned.ClosedAt)

	loaded, err := s.Load("m-2026-08-06-001")
	require.NoError(t, err)
	assert.Equal(t, StatusAbandoned, loaded.Status)

	events, _, err := s.LoadEvents("m-2026-08-06-001")
	require.NoError(t, err)
	require.Len(t, events, 2, "create + abandon, nothing else")
	assert.Equal(t, "abandon", events[1].Event)
	assert.Equal(t, "dead mission, never dispatched", events[1].Details["reason"])
}

// TestStore_Abandon_RedactsReasonInEventLog is the regression gate for
// the code-reviewer finding on the mission-abandon review pass: the
// abandon event's Details map wrote the operator-supplied reason raw,
// with no pass through PathRedactor. The mission tree is git-tracked
// and routinely pushed (CLAUDE.md's storage layout table), so a
// reason naming an absolute path under the operator's home directory
// leaked the username into shared history — the same defect class
// WriteDelegationSkeleton was fixed for (ethos-ersr, ethos-n4np). The
// assertion greps the raw bytes on disk for the home prefix, the way
// that defect was actually found, so it fails for any future leak
// path, not just the one field redacted today.
func TestStore_Abandon_RedactsReasonInEventLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()

	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract("m-2026-08-06-095")
	require.NoError(t, s.Create(c))

	leakyReason := "superseded by the contract in " + filepath.Join(home, ".tmp", "missions", "x.yaml")
	_, err := s.Abandon("m-2026-08-06-095", leakyReason)
	require.NoError(t, err)

	events, _, err := s.LoadEvents("m-2026-08-06-095")
	require.NoError(t, err)
	require.Len(t, events, 2)
	abandonEvent := events[1]
	require.Equal(t, "abandon", abandonEvent.Event)

	persistedReason, _ := abandonEvent.Details["reason"].(string)
	assert.NotContains(t, persistedReason, home,
		"the persisted reason must not carry the raw home path")
	assert.Equal(t, "superseded by the contract in ~/.tmp/missions/x.yaml", persistedReason)

	// Regression gate at the byte level: grep the on-disk log file
	// itself, not just the decoded struct, so a future leak path that
	// bypasses Details still fails this test. This Store is two-tree
	// (NewStoreWithRoots) with no session wired, so the append lands
	// in the reserved-session live log (DES-058), not the tracked
	// log.jsonl — audit.LiveMissionLogPath is the same helper
	// appendLiveEventLocked itself resolves against.
	logPath := audit.LiveMissionLogPath(repoRoot, "m-2026-08-06-095", sessionlessID)
	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), home,
		"the on-disk event log must never carry the raw home path")
}

// TestStore_Abandon_RedactorFailureLeavesMissionOpen is the
// regression gate for the ordering bug in the redaction fix's
// review pass: NewPathRedactor used to be constructed inside the
// locked section, AFTER writeContract had already stamped
// status: abandoned and closed_at on disk. If redactor construction
// failed there, Abandon returned an error but the contract on disk
// was already permanently terminal with no abandon event ever
// appended — a retry hit "already in terminal state" with no way
// back to open, and mission log never recorded why.
//
// The fix hoists NewPathRedactor above s.withLock, before any
// mutation, so this test asserts BOTH halves of the fix: Abandon
// must error, AND the mission must still be observably open
// afterward — a fail-open on either axis is the defect.
func TestStore_Abandon_RedactorFailureLeavesMissionOpen(t *testing.T) {
	// A relative $HOME is exactly the shape usablePrefix rejects
	// (NewPathRedactor requires an absolute path below the root), so
	// this reproduces "redactor construction fails" without needing
	// to fake a broken os.UserHomeDir.
	t.Setenv("HOME", "relative/path")
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()

	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract("m-2026-08-06-097")
	require.NoError(t, s.Create(c))

	_, err := s.Abandon("m-2026-08-06-097", "should never land on disk")
	require.Error(t, err, "Abandon must refuse when the redactor cannot be built")
	assert.Contains(t, err.Error(), "building path redactor")

	loaded, loadErr := s.Load("m-2026-08-06-097")
	require.NoError(t, loadErr)
	assert.Equal(t, StatusOpen, loaded.Status,
		"a refused abandon must leave the mission open, not wedged in a half-written terminal state")
	assert.Empty(t, loaded.ClosedAt, "closed_at must not be stamped when abandon never completes")

	events, _, err := s.LoadEvents("m-2026-08-06-097")
	require.NoError(t, err)
	require.Len(t, events, 1, "only the original create event — no abandon event, no partial write")
	assert.Equal(t, "create", events[0].Event)
}

// TestStore_Abandon_RefusesWhenRepoRootEmpty is the regression test
// for djb's round-2 probe: a Store with no repoRoot in scope must
// REFUSE abandon, not silently skip the delegations/ gate. The
// earlier `if s.repoRoot != ""` guard treated "no repo root to check"
// as equivalent to "checked, and found nothing" — which let a mission
// with a real spawned worker abandon cleanly with no error the moment
// the caller happened to construct the Store without a repoRoot.
//
// The test reproduces exactly that shape: a delegation record exists
// on disk under a real repoRoot, but the Store under test is
// constructed with repoRoot == "" (mission.NewStore(dir), the legacy
// single-tree constructor) — the same shape a misconfigured MCP
// handler or CLI invocation outside a repo checkout would produce.
// Fail-open would abandon the mission anyway, discarding the
// delegation's recoverable work with no error. Fail-closed refuses
// and tells the operator why.
func TestStore_Abandon_RefusesWhenRepoRootEmpty(t *testing.T) {
	globalRoot := t.TempDir()
	realRepoRoot := t.TempDir()

	// The Store under test has no repoRoot wired at all — the exact
	// misconfiguration the probe targets.
	s := NewStore(globalRoot)
	c := newContract("m-2026-08-06-090")
	require.NoError(t, s.Create(c))

	// A delegation record exists for this mission, but under a
	// repoRoot the Store under test was never told about. A
	// fail-open gate cannot see it; a fail-closed gate must refuse
	// on that basis alone, without ever needing to find it.
	_, err := WriteDelegationSkeleton(realRepoRoot, "m-2026-08-06-090", "d-2026-08-06-090", DelegationSkeleton{
		Tier:      "b",
		AgentType: "bwk",
	})
	require.NoError(t, err)

	_, err = s.Abandon("m-2026-08-06-090", "probing the fail-open gate")
	require.Error(t, err, "abandon must refuse, not silently skip the delegations gate, when repoRoot is empty")
	assert.Contains(t, err.Error(), "no repo root in scope")
	assert.Contains(t, err.Error(), "run abandon from inside the repo checkout")

	loaded, err := s.Load("m-2026-08-06-090")
	require.NoError(t, err)
	assert.Equal(t, StatusOpen, loaded.Status, "a refused abandon must not mutate the mission")
}

// TestStore_Abandon_RequiresReason asserts the CLI/MCP-visible
// contract: an empty or whitespace-only reason is refused before any
// disk mutation, so the audit trail can never carry an unexplained
// abandon event.
func TestStore_Abandon_RequiresReason(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract("m-2026-08-06-002")
	require.NoError(t, s.Create(c))

	for _, reason := range []string{"", "   ", "\t\n"} {
		_, err := s.Abandon("m-2026-08-06-002", reason)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reason is required")
	}

	loaded, err := s.Load("m-2026-08-06-002")
	require.NoError(t, err)
	assert.Equal(t, StatusOpen, loaded.Status, "a refused abandon must not mutate the mission")
}

// TestStore_Abandon_RefusesWhenDelegationExists is the trick case:
// a single delegation skeleton — even one that never closed — proves a
// worker was spawned, so Abandon must refuse even though the mission
// has no result artifact.
func TestStore_Abandon_RefusesWhenDelegationExists(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract("m-2026-08-06-003")
	require.NoError(t, s.Create(c))

	_, err := WriteDelegationSkeleton(repoRoot, "m-2026-08-06-003", "d-2026-08-06-001", DelegationSkeleton{
		Tier:      "b",
		AgentType: "bwk",
	})
	require.NoError(t, err)

	_, err = s.Abandon("m-2026-08-06-003", "trying to sneak past a spawned worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delegation record")
	assert.Contains(t, err.Error(), "mission close")

	loaded, err := s.Load("m-2026-08-06-003")
	require.NoError(t, err)
	assert.Equal(t, StatusOpen, loaded.Status, "a refused abandon must not mutate the mission")
}

// TestStore_Abandon_RefusesWhenDelegationClosed proves the gate looks
// at existence, not verdict: a delegation that already closed pass/fail
// still means a worker ran, which is recoverable work Abandon must not
// discard.
func TestStore_Abandon_RefusesWhenDelegationClosed(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract("m-2026-08-06-004")
	require.NoError(t, s.Create(c))

	recordPath, err := WriteDelegationSkeleton(repoRoot, "m-2026-08-06-004", "d-2026-08-06-002", DelegationSkeleton{
		Tier:      "b",
		AgentType: "bwk",
	})
	require.NoError(t, err)
	require.NoError(t, CloseDelegation(recordPath, DelegationVerdictPass, "done"))

	_, err = s.Abandon("m-2026-08-06-004", "ignoring the closed delegation")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delegation record")
}

// TestStore_Abandon_RefusesWhenResultExistsForAnyRound proves the
// result-artifact check is not limited to CurrentRound: a result
// recorded for a round the mission has since advanced past is still
// recoverable work.
func TestStore_Abandon_RefusesWhenResultExistsForAnyRound(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract("m-2026-08-06-005")
	c.Budget.Rounds = 2
	require.NoError(t, s.Create(c))
	submitRoundResult(t, s, c, VerdictEscalate)

	_, err := s.Abandon("m-2026-08-06-005", "ignoring a stale-round result")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "result artifact")
	assert.Contains(t, err.Error(), "round(s) 1")

	loaded, err := s.Load("m-2026-08-06-005")
	require.NoError(t, err)
	assert.Equal(t, StatusOpen, loaded.Status, "a refused abandon must not mutate the mission")
}

// TestStore_Abandon_RefusesAlreadyTerminal covers all four terminal
// states — closed, failed, escalated, and abandoned itself — none of
// which may be re-abandoned.
func TestStore_Abandon_RefusesAlreadyTerminal(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)

	cases := map[string]func(id string, c *Contract){
		StatusClosed: func(id string, c *Contract) {
			submitRoundResult(t, s, c, VerdictPass)
			_, err := s.Close(id, StatusClosed)
			require.NoError(t, err)
		},
		StatusFailed: func(id string, c *Contract) {
			submitRoundResult(t, s, c, VerdictFail)
			_, err := s.Close(id, StatusFailed)
			require.NoError(t, err)
		},
		StatusEscalated: func(id string, c *Contract) {
			submitRoundResult(t, s, c, VerdictEscalate)
			_, err := s.Close(id, StatusEscalated)
			require.NoError(t, err)
		},
		StatusAbandoned: func(id string, c *Contract) {
			_, err := s.Abandon(id, "first abandon")
			require.NoError(t, err)
		},
	}

	i := 0
	for status, setup := range cases {
		i++
		id := "m-2026-08-06-" + []string{"", "010", "011", "012", "013"}[i]
		t.Run(status, func(t *testing.T) {
			c := disjointContract(id)
			require.NoError(t, s.Create(c))
			setup(id, c)

			_, err := s.Abandon(id, "second attempt")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "already in terminal state")
		})
	}
}

// TestStore_Abandon_ExcludedFromWriteSetConflicts is the end-to-end
// proof of the actual bug fix: a mission created with an overlapping
// write_set, never dispatched, blocks a second Create until it is
// abandoned — after which the second Create succeeds.
func TestStore_Abandon_ExcludedFromWriteSetConflicts(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)

	a := newContract("m-2026-08-06-020")
	a.WriteSet = []string{"internal/shared/"}
	require.NoError(t, s.Create(a))

	b := newContract("m-2026-08-06-021")
	b.WriteSet = []string{"internal/shared/thing.go"}
	err := s.Create(b)
	require.Error(t, err, "an open dead mission must still block a conflicting create")

	_, err = s.Abandon("m-2026-08-06-020", "dead mission blocking real work")
	require.NoError(t, err)

	require.NoError(t, s.Create(b), "abandoning the dead mission must free its write_set")
}

// TestIsValidStatusFilter_AcceptsAbandoned asserts `mission list
// --status=abandoned` is a recognized filter value.
func TestIsValidStatusFilter_AcceptsAbandoned(t *testing.T) {
	assert.True(t, IsValidStatusFilter(StatusAbandoned))
}

// TestStore_Close_RejectsAbandonedAsTargetStatus proves Close cannot
// be used as a side door into the abandoned state — that would bypass
// Abandon's stricter, no-recoverable-work gate entirely.
func TestStore_Close_RejectsAbandonedAsTargetStatus(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract("m-2026-08-06-030")
	require.NoError(t, s.Create(c))
	submitRoundResult(t, s, c, VerdictPass)

	_, err := s.Close("m-2026-08-06-030", StatusAbandoned)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid close status")
}
