package autorepair

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authorityState34 reads each mission of env through the lifecycle readers and
// returns, per mission, the contract's decisions and whether corrections load.
func authorityState34(t *testing.T, env Env) map[string]*mission.Contract {
	t.Helper()
	s := mission.NewRepositoryStore(env.Policy, env.Global)
	out := map[string]*mission.Contract{}
	for _, dir := range missionDirs34(t, env) {
		id := filepath.Base(dir)
		c, err := s.Load(id)
		require.NoError(t, err, id)
		out[id] = c
	}
	return out
}

func TestRoundDecisions_ImportsPreV421Missions(t *testing.T) {
	for _, v := range []string{"v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4"} {
		t.Run(v, func(t *testing.T) {
			env := stage34(t, v)
			for id, c := range authorityState34(t, env) {
				require.Nil(t, c.RoundDecisions, "%s: corpus must be legacy", id)
			}

			out := run34(t, "round-decisions", env)
			assert.Equal(t, 2, out.Items, "missions imported")
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)

			s := mission.NewRepositoryStore(env.Policy, env.Global)
			for id, c := range authorityState34(t, env) {
				require.NotNil(t, c.RoundDecisions, id)
				assert.Len(t, c.RoundDecisions.Entries, c.CurrentRound-1, "%s: crossed rounds only", id)
				_, _, err := s.LoadCorrections(id)
				assert.NoError(t, err, "%s: corrections read", id)
				reads, err := s.ReadReflections(id)
				require.NoError(t, err, id)
				for _, r := range reads {
					assert.NotEqual(t, "legacy_unknown", r.DecisionState, "%s round %d", id, r.Round)
				}
				_, err = os.Stat(filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", id, "corrections.yaml"))
				assert.NoError(t, err, "%s: corrections.yaml written", id)
			}

			again := run34(t, "round-decisions", env)
			assert.Zero(t, again.Items, "second run is a no-op")
			assert.Empty(t, again.Residuals)
		})
	}
}

func TestRoundDecisions_CurrentSnapshotsChangeNothing(t *testing.T) {
	for _, v := range []string{"v4.21.0", "v4.22.1"} {
		t.Run(v, func(t *testing.T) {
			env := stage34(t, v)
			out := run34(t, "round-decisions", env)
			assert.Zero(t, out.Items)
			assert.Empty(t, out.Residuals)
			assert.False(t, out.Deferred)
		})
	}
}

// Q1: a mission whose crossed round has no reflection is fenced and recorded.
// Nothing is written for it, and a second run records the same single residual.
func TestRoundDecisions_IncompleteHistoryIsResidual(t *testing.T) {
	env := stage34(t, "v4.17.0")
	dir := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", "m-2026-11-09-001")
	contractBefore := read34(t, filepath.Join(dir, "contract.yaml"))
	require.NoError(t, os.Remove(filepath.Join(dir, "reflections.yaml")))

	out := run34(t, "round-decisions", env)
	assert.Equal(t, 1, out.Items, "the undamaged mission is still imported")
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "round-decisions", out.Residuals[0].Step)
	assert.Equal(t, "m-2026-11-09-001", out.Residuals[0].ID)
	assert.Contains(t, out.Residuals[0].Reason, "incomplete")

	assert.Equal(t, contractBefore, read34(t, filepath.Join(dir, "contract.yaml")), "no decisions written")
	_, err := os.Stat(filepath.Join(dir, "corrections.yaml"))
	assert.True(t, os.IsNotExist(err), "no corrections written for the fenced mission")

	s := mission.NewRepositoryStore(env.Policy, env.Global)
	reads, err := s.ReadReflections("m-2026-11-09-001")
	require.NoError(t, err)
	require.NotEmpty(t, reads)
	assert.Equal(t, "legacy_unknown", reads[0].DecisionState, "reads show authority unavailable")

	again := run34(t, "round-decisions", env)
	assert.Zero(t, again.Items)
	assert.Equal(t, out.Residuals, again.Residuals, "same residual, not duplicated")
	assert.Equal(t, contractBefore, read34(t, filepath.Join(dir, "contract.yaml")))
}

func TestRoundDecisions_OutsideRepositoryDoesNothing(t *testing.T) {
	assert.Equal(t, Outcome{}, run34(t, "round-decisions", Env{Log: &recLog34{}}))
}

// H1: the current binary itself leaves corrections.yaml without round
// decisions, when Correct runs on a legacy closed mission. The step must finish
// that import, keeping the corrections as they are.
func TestRoundDecisions_CorrectionsWithoutDecisionsIsImported(t *testing.T) {
	env := stage34(t, "v4.17.0")
	const id = "m-2026-11-09-001"
	dir := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", id)
	s := mission.NewRepositoryStore(env.Policy, env.Global)
	require.NoError(t, s.Correct(id, mission.Correction{
		Mission: id, Kind: mission.CorrectionFactual, Author: "claude",
		Claim: "the mission failed", Corrected: "the mission passed",
	}))
	corrections := read34(t, filepath.Join(dir, "corrections.yaml"))
	c, err := s.Load(id)
	require.NoError(t, err)
	require.Nil(t, c.RoundDecisions, "Correct leaves the contract without decisions")

	out := run34(t, "round-decisions", env)
	assert.Empty(t, out.Residuals)
	assert.Equal(t, 2, out.Items)
	c, err = s.Load(id)
	require.NoError(t, err)
	require.NotNil(t, c.RoundDecisions)
	assert.Equal(t, corrections, read34(t, filepath.Join(dir, "corrections.yaml")), "corrections unchanged")

	again := run34(t, "round-decisions", env)
	assert.Zero(t, again.Items)
	assert.Empty(t, again.Residuals)
}

// snapshotTree34 returns every file under dir with its bytes, so a second run
// can be shown to change neither the file list nor any byte.
func snapshotTree34(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[p] = string(b)
		return nil
	}))
	return out
}

// L5: the second run changes neither the file list nor any byte.
func TestRoundDecisions_SecondRunLeavesTheTreeUnchanged(t *testing.T) {
	env := stage34(t, "v4.17.0")
	run34(t, "round-decisions", env)
	dir := filepath.Join(env.Roots.Store, ".punt-labs", "ethos")
	before := snapshotTree34(t, dir)
	run34(t, "round-decisions", env)
	assert.Equal(t, before, snapshotTree34(t, dir))
}

// M2: the step prints nothing, even when a durability warning fires.
func TestRoundDecisions_PrintsNothing(t *testing.T) {
	env := stage34(t, "v4.17.0")
	stderr := captureStderr34(t, func() { run34(t, "round-decisions", env) })
	assert.Empty(t, stderr)
}

// M4: a busy lock defers and writes no residual; the free mission is imported.
func TestRoundDecisions_BusyLockDefers(t *testing.T) {
	env := stage34(t, "v4.17.0")
	id := filepath.Base(missionDirs34(t, env)[0])
	var out Outcome
	require.NoError(t, mission.WithRecordLockTry(env.Policy, id, func() error {
		out = run34(t, "round-decisions", env)
		return nil
	}))
	assert.True(t, out.Deferred)
	assert.Empty(t, out.Residuals)
	assert.Equal(t, 1, out.Items)
}

// M4: a contract that cannot be decoded is a permanent condition, so it is a
// residual and never defers; the reason names the class, not the user's action.
func TestRoundDecisions_UnloadableContractIsResidual(t *testing.T) {
	env := stage34(t, "v4.17.0")
	dir := missionDirs34(t, env)[0]
	write34(t, filepath.Join(dir, "contract.yaml"), "broken: [yaml")

	out := run34(t, "round-decisions", env)
	assert.False(t, out.Deferred)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, filepath.Base(dir), out.Residuals[0].ID)
	assert.Contains(t, out.Residuals[0].Reason, "contract cannot be loaded")
	assert.Equal(t, 1, out.Items, "the other mission is imported")
}

// L2: the reason names the class of fence.
func TestRoundDecisions_ReasonNamesTheClass(t *testing.T) {
	env := stage34(t, "v4.17.0")
	dir := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", "m-2026-11-09-001")
	write34(t, filepath.Join(dir, "corrections.yaml"), "version: 7\n")

	out := run34(t, "round-decisions", env)
	require.Len(t, out.Residuals, 1)
	assert.Contains(t, out.Residuals[0].Reason, "corrections authority is corrupt")
	assert.NotContains(t, out.Residuals[0].Reason, "incomplete")
}

// M5: step 3 hashes decision IDs over reflections before step 4 scrubs them.
// The stored IDs must survive the scrub unchanged, because later steps copy them
// from the contract and never recompute them from the scrubbed file.
func TestRoundDecisions_StoredIDsSurviveTheScrub(t *testing.T) {
	env := stage34(t, "v4.17.0")
	plantAll34(t, env)
	cmd := testhelpers.GitCommand("add", "-A", "--", ".")
	cmd.Dir = env.Roots.Checkout
	b, err := cmd.CombinedOutput()
	require.NoError(t, err, string(b))

	ids := func() map[string][]string {
		out := map[string][]string{}
		for id, c := range authorityState34(t, env) {
			require.NotNil(t, c.RoundDecisions, id)
			for _, e := range c.RoundDecisions.Entries {
				out[id] = append(out[id], e.ID)
			}
		}
		return out
	}
	run34(t, "round-decisions", env)
	before := ids()
	require.NotEmpty(t, before["m-2026-11-09-001"])

	scrub := run34(t, "scrub-records", env)
	require.Empty(t, scrub.Residuals)
	require.GreaterOrEqual(t, scrub.Items, 1)
	assert.Equal(t, before, ids(), "the scrub does not change a stored decision ID")
}
