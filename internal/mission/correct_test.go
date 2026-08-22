//go:build !windows

package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closedMission creates a fresh two-tree mission, submits a passing
// result for round 1, and closes it. Returns the store and mission ID
// so callers can exercise Store.Correct against a well-formed closed
// mission without repeating the create/result/close boilerplate.
func closedMission(t *testing.T, id string) (*Store, *Contract) {
	t.Helper()
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract(id)
	require.NoError(t, s.Create(c))
	submitRoundResult(t, s, c, VerdictPass)
	_, err := s.Close(id, StatusClosed)
	require.NoError(t, err)
	loaded, err := s.Load(id)
	require.NoError(t, err)
	return s, loaded
}

func TestStore_Correct_RefusesOnOpenMission(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)
	c := newContract("m-2026-08-22-101")
	require.NoError(t, s.Create(c))

	err := s.Correct("m-2026-08-22-101", Correction{
		Mission:   "m-2026-08-22-101",
		Kind:      CorrectionFactual,
		Author:    "claude",
		Claim:     "the mission failed",
		Corrected: "the mission actually passed",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open")
	assert.Contains(t, err.Error(), "corrections apply only to closed missions")
}

func TestStore_Correct_AppendsEventWithoutMutatingContract(t *testing.T) {
	s, c := closedMission(t, "m-2026-08-22-102")

	path := mustContractPath(t, s, c.MissionID)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	require.NoError(t, s.Correct(c.MissionID, Correction{
		Mission:   c.MissionID,
		Kind:      CorrectionFabrication,
		Author:    "claude",
		Claim:     "make check passed",
		Corrected: "make check was never run",
	}))

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after,
		"Correct must never rewrite contract.yaml — its entire effect is an event log append")

	events, _, err := s.LoadEvents(c.MissionID)
	require.NoError(t, err)
	var found bool
	for _, e := range events {
		if e.Event == EventCorrect {
			found = true
			assert.Equal(t, "claude", e.Actor)
			assert.Equal(t, "fabrication", e.Details["kind"])
		}
	}
	assert.True(t, found, "correct event must appear in the log")
}

func TestStore_Correct_RefusesGlobalLayerMission(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot)

	id := "m-2026-08-01-001"
	ps := s.pathSetFor(id, layerGlobal)
	require.NoError(t, os.MkdirAll(filepath.Dir(ps.contract), 0o700))
	body := `mission_id: m-2026-08-01-001
status: closed
created_at: "2026-08-01T00:00:00Z"
updated_at: "2026-08-01T00:00:00Z"
closed_at: "2026-08-01T01:00:00Z"
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: "2026-08-01T00:00:00Z"
write_set:
  - tests/legacy/
success_criteria:
  - make check passes
budget:
  rounds: 1
current_round: 1
`
	require.NoError(t, os.WriteFile(ps.contract, []byte(body), 0o600))

	err := s.Correct(id, Correction{
		Mission:   id,
		Kind:      CorrectionFactual,
		Author:    "claude",
		Claim:     "something",
		Corrected: "something else",
	})
	require.Error(t, err)
	assert.Equal(t,
		"cannot correct a global-layer mission from a repo-layer session",
		err.Error())
}

// TestStore_Correct_SealsOnSuccess asserts the correction is readable
// from a fresh Store opened after Correct returns, with no
// intervening seal call. Store.Correct itself does not seal (the
// mission package cannot import internal/hook without cycling); this
// test proves the durability property that actually matters at the
// package boundary — LoadEvents reads the union of sealed chunks and
// the live tail, so a correction is visible immediately regardless of
// when a seal runs.
func TestStore_Correct_SealsOnSuccess(t *testing.T) {
	s, c := closedMission(t, "m-2026-08-22-103")

	require.NoError(t, s.Correct(c.MissionID, Correction{
		Mission:   c.MissionID,
		Kind:      CorrectionDecision,
		Author:    "claude",
		Corrected: "escalation resolved: re-scope and re-dispatch",
	}))

	// A brand-new Store instance pointed at the same roots, so nothing
	// about the original *Store's in-memory state can leak through.
	fresh := NewStoreWithRoots(s.repoRoot, s.root)
	events, _, err := fresh.LoadEvents(c.MissionID)
	require.NoError(t, err)
	var found bool
	for _, e := range events {
		if e.Event == EventCorrect {
			found = true
		}
	}
	assert.True(t, found, "correction must be readable from a fresh Store with no intervening seal")
}

func TestStore_Correct_RequiresClaimUnlessDecision(t *testing.T) {
	s, c := closedMission(t, "m-2026-08-22-104")

	err := s.Correct(c.MissionID, Correction{
		Mission:   c.MissionID,
		Kind:      CorrectionFactual,
		Author:    "claude",
		Corrected: "it was actually fine",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claim is required unless kind is decision")

	// Decision kind needs no claim.
	require.NoError(t, s.Correct(c.MissionID, Correction{
		Mission:   c.MissionID,
		Kind:      CorrectionDecision,
		Author:    "claude",
		Corrected: "leader ruling: re-scope",
	}))
}

func TestStore_Correct_RejectsRoundBeyondCurrent(t *testing.T) {
	s, c := closedMission(t, "m-2026-08-22-105")
	require.Equal(t, 1, c.CurrentRound)

	err := s.Correct(c.MissionID, Correction{
		Mission:   c.MissionID,
		Round:     2,
		Kind:      CorrectionFactual,
		Author:    "claude",
		Claim:     "round 2 happened",
		Corrected: "round 2 never ran",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds mission")

	// Round 0 (whole-mission sentinel) and round == CurrentRound are
	// both allowed.
	require.NoError(t, s.Correct(c.MissionID, Correction{
		Mission:   c.MissionID,
		Round:     0,
		Kind:      CorrectionFactual,
		Author:    "claude",
		Claim:     "whole mission was fine",
		Corrected: "whole mission had a stale worktree",
	}))
	require.NoError(t, s.Correct(c.MissionID, Correction{
		Mission:   c.MissionID,
		Round:     1,
		Kind:      CorrectionFactual,
		Author:    "claude",
		Claim:     "round 1 passed cleanly",
		Corrected: "round 1's evidence entry was fabricated",
	}))
}

// TestStore_Correct_RendersInShowAndLog asserts the correction event
// carries everything a rendering surface (mission show, mission log,
// mission results, the MCP formatter, internal/ui) needs to display
// it, and that LoadCorrections reconstructs the same fields from the
// log after a JSON round-trip.
func TestStore_Correct_RendersInShowAndLog(t *testing.T) {
	s, c := closedMission(t, "m-2026-08-22-106")

	require.NoError(t, s.Correct(c.MissionID, Correction{
		Mission:    c.MissionID,
		Round:      1,
		Kind:       CorrectionFabrication,
		Author:     "claude",
		Claim:      "make check (full suite): fail — pre-existing, unrelated",
		Corrected:  "make check failed because of a stale worktree base, not a pre-existing defect",
		Supersedes: "",
		Evidence: []EvidenceCheck{
			{Name: "re-ran make check on a fresh worktree", Status: EvidenceStatusPass},
		},
	}))

	events, _, err := s.LoadEvents(c.MissionID)
	require.NoError(t, err)
	var ev Event
	var found bool
	for _, e := range events {
		if e.Event == EventCorrect {
			ev = e
			found = true
		}
	}
	require.True(t, found)
	assert.NotEmpty(t, ev.TS, "correct events must carry a timestamp for chronological rendering")
	assert.Equal(t, "claude", ev.Actor)
	assert.Equal(t, "fabrication", ev.Details["kind"])
	assert.Equal(t,
		"make check failed because of a stale worktree base, not a pre-existing defect",
		ev.Details["corrected"])
	assert.Equal(t,
		"make check (full suite): fail — pre-existing, unrelated",
		ev.Details["claim"])

	corrections, err := s.LoadCorrections(c.MissionID)
	require.NoError(t, err)
	require.Len(t, corrections, 1)
	got := corrections[0]
	assert.Equal(t, c.MissionID, got.Mission)
	assert.Equal(t, 1, got.Round)
	assert.Equal(t, CorrectionFabrication, got.Kind)
	assert.Equal(t, "claude", got.Author)
	assert.Equal(t, "make check (full suite): fail — pre-existing, unrelated", got.Claim)
	assert.Equal(t, "make check failed because of a stale worktree base, not a pre-existing defect", got.Corrected)
	require.Len(t, got.Evidence, 1)
	assert.Equal(t, "re-ran make check on a fresh worktree", got.Evidence[0].Name)
	assert.Equal(t, EvidenceStatusPass, got.Evidence[0].Status)
}

func TestValidateCorrectionAuthor(t *testing.T) {
	loader := &mapIdentityLoader{m: map[string]*EvaluatorIdentity{
		"claude": {Handle: "claude"},
	}}

	require.NoError(t, ValidateCorrectionAuthor("claude", loader))

	err := ValidateCorrectionAuthor("ghost", loader)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")

	err = ValidateCorrectionAuthor("claude", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity loader not configured")
}

func TestCorrection_Validate(t *testing.T) {
	base := func() Correction {
		return Correction{
			Mission:   "m-2026-08-22-107",
			Kind:      CorrectionFactual,
			Author:    "claude",
			Claim:     "a claim",
			Corrected: "the truth",
		}
	}

	valid := base()
	require.NoError(t, valid.Validate())

	badKind := base()
	badKind.Kind = "bogus"
	require.Error(t, badKind.Validate())

	noClaim := base()
	noClaim.Claim = ""
	err := noClaim.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claim is required")

	decisionNoClaim := base()
	decisionNoClaim.Kind = CorrectionDecision
	decisionNoClaim.Claim = ""
	require.NoError(t, decisionNoClaim.Validate())

	noCorrected := base()
	noCorrected.Corrected = ""
	require.Error(t, noCorrected.Validate())

	negRound := base()
	negRound.Round = -1
	require.Error(t, negRound.Validate())

	var nilC *Correction
	err = nilC.Validate()
	require.Error(t, err)
}

// TestStore_Correct_MissionMismatchRefused is a small structural
// regression: Correction.Mission must match the missionID argument,
// symmetric with AppendResult's cross-check.
func TestStore_Correct_MissionMismatchRefused(t *testing.T) {
	s, c := closedMission(t, "m-2026-08-22-108")

	err := s.Correct(c.MissionID, Correction{
		Mission:   "m-2026-08-22-999",
		Kind:      CorrectionFactual,
		Author:    "claude",
		Claim:     "x",
		Corrected: "y",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match target mission")
}

// TestStore_Correct_AcceptsEveryTerminalStatus proves Correct's guard
// is genuinely the inverse of every other write path: it accepts
// closed, failed, escalated, AND abandoned, not just closed.
func TestStore_Correct_AcceptsEveryTerminalStatus(t *testing.T) {
	for i, status := range []string{StatusClosed, StatusFailed, StatusEscalated, StatusAbandoned} {
		id := fmt.Sprintf("m-2026-08-22-2%02d", i)
		repoRoot := t.TempDir()
		globalRoot := t.TempDir()
		s := NewStoreWithRoots(repoRoot, globalRoot)
		c := newContract(id)
		require.NoError(t, s.Create(c))

		if status == StatusAbandoned {
			// Abandon refuses a mission with delegations or result
			// artifacts, so it takes the create-only path rather than
			// submitRoundResult + Close like the other three statuses.
			_, err := s.Abandon(id, "reason")
			require.NoError(t, err)
		} else {
			submitRoundResult(t, s, c, VerdictPass)
			_, err := s.Close(id, status)
			require.NoError(t, err)
		}

		err := s.Correct(id, Correction{
			Mission:   id,
			Kind:      CorrectionFactual,
			Author:    "claude",
			Claim:     "x",
			Corrected: "y",
		})
		require.NoError(t, err, "status %q must accept a correction", status)
	}
}

func TestDecodeCorrectionStrict(t *testing.T) {
	body := `mission: m-2026-08-22-301
round: 1
kind: factual
author: claude
claim: something was true
corrected: it no longer is
evidence:
  - name: re-ran the check
    status: pass
`
	c, err := DecodeCorrectionStrict([]byte(body), "test.yaml")
	require.NoError(t, err)
	assert.Equal(t, "m-2026-08-22-301", c.Mission)
	assert.Equal(t, CorrectionFactual, c.Kind)
	require.Len(t, c.Evidence, 1)

	_, err = DecodeCorrectionStrict([]byte(body+"\nbogus_field: 1\n"), "test.yaml")
	require.Error(t, err)

	_, err = DecodeCorrectionStrict([]byte(body+"---\nmission: m2\n"), "test.yaml")
	require.Error(t, err)
}
