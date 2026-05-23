package hook

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFindDelegationByID_HitReturnsRecordAndMission pins the happy
// path of the shared cross-mission lookup: a delegation that exists
// under one of <repo>/.ethos/missions/*/delegations/<id>/ resolves to
// (record, missionID, nil). The depth gate and the inheritance walk
// both depend on this contract.
func TestFindDelegationByID_HitReturnsRecordAndMission(t *testing.T) {
	repo := t.TempDir()
	missionID := "m-2026-05-23-310"
	delegationID := "d-2026-05-23-310"
	_, err := mission.WriteDelegationSkeleton(repo, missionID, delegationID, mission.DelegationSkeleton{
		Tier:      mission.TierB,
		AgentType: "djb",
	})
	require.NoError(t, err)

	d, mID, err := findDelegationByID(repo, delegationID)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, delegationID, d.ID)
	assert.Equal(t, missionID, mID,
		"the returned missionID must point at the mission tree where the record was found")
}

// TestFindDelegationByID_HitInSecondMissionTree pins the multi-mission
// scan: a delegation under M_other (NOT M_first) is still found. This
// is the contract the Bugbot MED on PR #328 required — a single-mission
// loader keyed on the inherited missionID would refuse here.
func TestFindDelegationByID_HitInSecondMissionTree(t *testing.T) {
	repo := t.TempDir()

	// Stage an empty mission directory to ensure the scan does not
	// short-circuit on the first dir entry.
	firstMission := "m-2026-05-23-311"
	require.NoError(t, os.MkdirAll(
		filepath.Join(repo, ".ethos", "missions", firstMission, "delegations"),
		0o700,
	))

	// Delegation lives in a different mission tree.
	otherMission := "m-2026-05-23-312"
	delegationID := "d-2026-05-23-312"
	_, err := mission.WriteDelegationSkeleton(repo, otherMission, delegationID, mission.DelegationSkeleton{
		Tier:      mission.TierB,
		AgentType: "bwk",
	})
	require.NoError(t, err)

	d, mID, err := findDelegationByID(repo, delegationID)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, otherMission, mID,
		"the scan must continue past empty mission trees and report the tree where the record was actually found")
}

// TestFindDelegationByID_MissingMissionsDirReturnsErrNotExist pins
// the "no tree at all" shape. The doc comment promises callers can
// errors.Is-test the returned error for fs.ErrNotExist, so the
// missions-dir-absent branch must wrap the sentinel correctly.
func TestFindDelegationByID_MissingMissionsDirReturnsErrNotExist(t *testing.T) {
	repo := t.TempDir()

	d, mID, err := findDelegationByID(repo, "d-anything")
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrNotExist),
		"missing missions dir must wrap fs.ErrNotExist so the inheritance walker can distinguish it from a real I/O fault")
	assert.Nil(t, d)
	assert.Empty(t, mID)
}

// TestFindDelegationByID_TreeExistsButNoMatchReturnsErrNotExist pins
// the "tree exists, no matching record" shape. Same wrap semantics as
// the missing-tree case — callers cannot distinguish the two from the
// returned error and should not need to.
func TestFindDelegationByID_TreeExistsButNoMatchReturnsErrNotExist(t *testing.T) {
	repo := t.TempDir()

	// Stage two mission trees with delegations, neither containing
	// the queried ID.
	for _, m := range []string{"m-2026-05-23-313", "m-2026-05-23-314"} {
		_, err := mission.WriteDelegationSkeleton(repo, m, "d-"+m[2:], mission.DelegationSkeleton{
			Tier:      mission.TierB,
			AgentType: "mdm",
		})
		require.NoError(t, err)
	}

	d, mID, err := findDelegationByID(repo, "d-2026-05-23-999")
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrNotExist),
		"tree-but-no-match must wrap fs.ErrNotExist so the depth walker surfaces it as a refusal, not a malformed-I/O fault")
	assert.Nil(t, d)
	assert.Empty(t, mID)
}

// TestFindDelegationByID_RealIOFaultNotWrappedAsNotExist pins the
// third documented shape: a real I/O fault (not fs.ErrNotExist) is
// returned with the underlying error wrapped, so callers can NOT
// errors.Is-test it as fs.ErrNotExist. A missing-tree mis-classified
// as an I/O fault (or vice versa) would change the depth gate from
// "refuse with a clear reason" to "silently treat as tree-absent."
//
// Simulated by removing read perms on the missions dir so os.ReadDir
// returns EACCES. Skipped when running as root because root can read
// regardless of permission bits.
func TestFindDelegationByID_RealIOFaultNotWrappedAsNotExist(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-bit enforcement is bypassed when running as root")
	}
	repo := t.TempDir()
	missionsDir := filepath.Join(repo, ".ethos", "missions")
	require.NoError(t, os.MkdirAll(missionsDir, 0o700))
	require.NoError(t, os.Chmod(missionsDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(missionsDir, 0o700) })

	d, mID, err := findDelegationByID(repo, "d-anything")
	require.Error(t, err)
	assert.False(t, errors.Is(err, fs.ErrNotExist),
		"a real I/O fault (EACCES) must NOT be reported as fs.ErrNotExist — that would let the depth gate misclassify it as 'no tree'")
	assert.Nil(t, d)
	assert.Empty(t, mID)
}
