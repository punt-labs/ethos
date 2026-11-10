package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

const (
	ownedMission   = "m-2026-11-09-001"
	unownedMission = "m-2026-11-09-002"
)

// ownMission writes a live session audit line naming id, the ownership
// signal the repository keeps for a mission it created.
func ownMission(t *testing.T, env Env, id string) {
	t.Helper()
	path := audit.LiveAuditPath(env.Roots.Store, "sess-owner")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"ts":"2026-11-09T23:02:12Z","session":"sess-owner","tool":"Bash","contract_id":"` + id + `"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func globalMissionFiles(t *testing.T, env Env, id string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(env.Global, "missions", id+".*"))
	require.NoError(t, err)
	var out []string
	for _, p := range m {
		if filepath.Ext(p) != ".lock" { // lock files are step 15's
			out = append(out, p)
		}
	}
	return out
}

func TestGlobalMissions(t *testing.T) {
	for _, v := range []string{"v3.11.0", "v3.12.0"} {
		t.Run(v, func(t *testing.T) {
			env, log := corpusEnv(t, v)
			ownMission(t, env, ownedMission)
			require.NotEmpty(t, globalMissionFiles(t, env, ownedMission))

			out, err := runGlobalMissions(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 1, out.Items)
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)
			assert.NotEmpty(t, log.steps, "the step logs what it moves")

			s := mission.NewStoreWithRoots(env.Roots.Store, filepath.Dir(env.Global))
			c, err := s.Load(ownedMission)
			require.NoError(t, err)
			assert.Equal(t, ownedMission, c.MissionID)
			assert.FileExists(t, filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", ownedMission, "contract.yaml"))
			assert.Empty(t, globalMissionFiles(t, env, ownedMission), "global copy deleted once the redacted copy exists")
			assert.NotEmpty(t, globalMissionFiles(t, env, unownedMission), "a mission this repository does not own stays global")

			out, err = runGlobalMissions(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items, "second run changes nothing")
		})
	}
}

func TestGlobalMissionsNothingGlobal(t *testing.T) {
	for _, v := range []string{"v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4", "v4.21.0", "v4.22.1"} {
		t.Run(v, func(t *testing.T) {
			env, _ := corpusEnv(t, v)
			ownMission(t, env, ownedMission)
			out, err := runGlobalMissions(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items)
			assert.Empty(t, out.Residuals)
			s := mission.NewStoreWithRoots(env.Roots.Store, filepath.Dir(env.Global))
			_, err = s.Load(ownedMission)
			require.NoError(t, err, "repository mission stays readable")
		})
	}
}

func TestGlobalMissionsUnparsableIsResidual(t *testing.T) {
	env, _ := corpusEnv(t, "v3.11.0")
	ownMission(t, env, ownedMission)
	ownMission(t, env, unownedMission)
	// A torn log line cannot be redacted, so mission 001 cannot be imported.
	require.NoError(t, os.WriteFile(filepath.Join(env.Global, "missions", ownedMission+".jsonl"), []byte(`{"event":"clo`+"\n"), 0o600))

	out, err := runGlobalMissions(context.Background(), env)
	require.NoError(t, err, "the step finishes")
	assert.Equal(t, 1, out.Items, "the other owned mission is still imported")
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, Residual{Step: "global-missions", ID: ownedMission, Reason: out.Residuals[0].Reason}, out.Residuals[0])
	assert.NotContains(t, out.Residuals[0].Reason, "ethos mission migrate")
	assert.NotEmpty(t, globalMissionFiles(t, env, ownedMission), "an unconverted mission is not deleted")
}

func TestGlobalMissionsOutsideRepository(t *testing.T) {
	env, _ := corpusEnv(t, "v3.11.0")
	env.Roots.Store = ""
	out, err := runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	assert.NotEmpty(t, globalMissionFiles(t, env, ownedMission))
}
