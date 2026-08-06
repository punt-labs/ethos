package mission

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestScaffoldContractYAML_Decodes proves the scaffold survives the
// real strict decoder unchanged — the class of friction ethos-q682
// reports (an unmarshal error revealing one missing field at a time)
// cannot recur against this exact output.
func TestScaffoldContractYAML_Decodes(t *testing.T) {
	_, err := DecodeContractStrict([]byte(ScaffoldContractYAML()), "scaffold")
	require.NoError(t, err)
}

// TestScaffoldContractYAML_ValidatesOnceServerFieldsApplied proves the
// scaffold's own fields — leader, worker, evaluator.handle, write_set,
// success_criteria, budget — are individually well-formed. The
// mission_id/status/timestamps/current_round fields are deliberately
// absent from the scaffold (the store computes them); this test fills
// them the same way Store.Create does before calling Validate, so a
// scaffold with only its CHANGE_ME markers edited is provably one
// `ethos mission create --file` call away from a persisted contract.
func TestScaffoldContractYAML_ValidatesOnceServerFieldsApplied(t *testing.T) {
	c, err := DecodeContractStrict([]byte(ScaffoldContractYAML()), "scaffold")
	require.NoError(t, err)

	c.MissionID = "m-2026-04-07-001"
	c.Status = StatusOpen
	c.CreatedAt = "2026-04-07T21:30:00Z"
	c.UpdatedAt = "2026-04-07T21:30:00Z"
	c.Evaluator.PinnedAt = "2026-04-07T21:30:00Z"
	c.CurrentRound = 1

	require.NoError(t, c.Validate())
	require.NotEqual(t, c.Worker, c.Evaluator.Handle,
		"scaffold placeholders must satisfy the self-verification guard")
}

// TestScaffoldResultYAML_Decodes proves the scaffold survives the real
// strict decoder unchanged.
func TestScaffoldResultYAML_Decodes(t *testing.T) {
	_, err := DecodeResultStrict([]byte(ScaffoldResultYAML()), "scaffold")
	require.NoError(t, err)
}

// TestScaffoldResultYAML_Validates proves the scaffold's own fields —
// mission, round, author, verdict, confidence, files_changed,
// evidence — are individually well-formed as written, with no
// additional massaging required: unlike the contract scaffold, a
// result carries no store-computed fields, so a scaffold with only
// its CHANGE_ME markers (and mission/round) edited to match a real
// mission is immediately submittable via
// `ethos mission result <id> --file <this-file>`.
func TestScaffoldResultYAML_Validates(t *testing.T) {
	r, err := DecodeResultStrict([]byte(ScaffoldResultYAML()), "scaffold")
	require.NoError(t, err)
	require.NoError(t, r.Validate())
}
