package mission

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test helpers shared by tests that run on every platform. A test that needs a
// platform feature lives in a file with its own build constraint, and must not
// hold a helper an unconstrained test uses.

// newContract returns a fresh, valid contract with the given mission ID
// for use in store tests.
func newContract(missionID string) *Contract {
	c := validContract()
	c.MissionID = missionID
	return &c
}

// resultFor returns a minimal valid result for the given mission's
// current round with the given verdict. Tests that only care about
// the gate's presence/absence behavior use this; the more detailed
// tests build results inline.
func resultFor(c *Contract, verdict string) *Result {
	return &Result{
		Mission:    c.MissionID,
		Round:      c.CurrentRound,
		Author:     c.Worker,
		Verdict:    verdict,
		Confidence: 0.8,
		Evidence: []EvidenceCheck{
			{Name: "make check", Status: EvidenceStatusPass},
		},
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// reflectionFor returns a fresh, valid reflection for the given round
// with the given recommendation. Tests use this to keep the table
// rows compact and the assertions focused on the gate behavior, not
// on building the input.
func reflectionFor(missionID string, round int, rec string) *Reflection {
	return &Reflection{
		Mission:        missionID,
		Round:          round,
		Author:         "claude",
		Converging:     true,
		Signals:        []string{"all green"},
		Recommendation: rec,
		Reason:         "round " + fmt.Sprint(round) + " " + rec,
	}
}

// mustContractPath returns the contract path for missionID or fails
// the test. Mirrors the old string-returning helper that tests used
// before mission m-2026-05-22-027 fix 3 changed the signature to
// (string, error); keeps test churn confined to one helper per
// per-artifact path. The unwrapped path is what every test wants.
func mustContractPath(t *testing.T, s *Store, missionID string) string {
	t.Helper()
	p, err := s.contractPath(missionID)
	require.NoError(t, err)
	return p
}

func mustResultsPath(t *testing.T, s *Store, missionID string) string {
	t.Helper()
	p, err := s.resultsPath(missionID)
	require.NoError(t, err)
	return p
}

// submitRoundResult persists a minimal valid result for a mission's
// current round so the test can drive Store.Close through the
// Phase 3.6 gate. The helper is a one-liner for every pre-3.6 test
// whose only interest in the result artifact is "close is allowed".
// A round-aware variant (submitResultForRound) covers the rarer case
// of exercising the gate at a non-current round.
func submitRoundResult(t *testing.T, s *Store, c *Contract, verdict string) {
	t.Helper()
	r := &Result{
		Mission:    c.MissionID,
		Round:      c.CurrentRound,
		Author:     c.Worker,
		Verdict:    verdict,
		Confidence: 0.8,
		Evidence: []EvidenceCheck{
			{Name: "go test ./...", Status: EvidenceStatusPass},
		},
	}
	require.NoError(t, s.AppendResult(c.MissionID, r))
}

func mustReflectionsPath(t *testing.T, s *Store, missionID string) string {
	t.Helper()
	p, err := s.reflectionsPath(missionID)
	require.NoError(t, err)
	return p
}

// appendTraceSummary prepares and appends one summary line the way Close and
// Abandon do: the test-side stand-in for the two steps.
func appendTraceSummary(s *Store, c *Contract, r *Result) error {
	data, _, err := s.prepareTraceSummary(c, r)
	if err != nil || data == nil {
		return err
	}
	return s.appendTraceLine(data)
}
