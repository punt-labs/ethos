package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

const (
	ownedMission   = "m-2026-11-09-001"
	unownedMission = "m-2026-11-09-002"
	plantedToken   = "ghp_FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"
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

// plantSecrets puts a credential and a home path into the global contract
// and log of mission id, so the import's redaction has something to remove.
func plantSecrets(t *testing.T, env Env, id string) {
	t.Helper()
	home := os.Getenv("HOME")
	cp := filepath.Join(env.Global, "missions", id+".yaml")
	b, err := os.ReadFile(cp)
	require.NoError(t, err)
	s := strings.Replace(string(b), "context: ", "context: token "+plantedToken+" in "+home+"/notes; ", 1)
	require.NotEqual(t, string(b), s)
	require.NoError(t, os.WriteFile(cp, []byte(s), 0o600))
	lp := filepath.Join(env.Global, "missions", id+".jsonl")
	f, err := os.OpenFile(lp, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"ts":"2026-11-09T23:02:13Z","event":"update","actor":"claude","details":{"note":"` + plantedToken + ` at ` + home + `/x"}}` + "\n")
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

func repoMissionDir(env Env, id string) string {
	return filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", id)
}

// readAll concatenates every file under dir, for redaction assertions.
func readAll(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	for p := range treeDigest(t, dir) {
		b, err := os.ReadFile(filepath.Join(dir, p))
		require.NoError(t, err)
		sb.Write(b)
	}
	return sb.String()
}

// TestGlobalMissions: an owned global mission is imported, redacted and
// verified; the global files are never touched; the import is recorded once
// as a residual; the second run changes nothing anywhere.
func TestGlobalMissions(t *testing.T) {
	for _, v := range []string{"v3.11.0", "v3.12.0"} {
		t.Run(v, func(t *testing.T) {
			env, log := corpusEnvLogged(t, v)
			ownMission(t, env, ownedMission)
			plantSecrets(t, env, ownedMission)
			require.NotEmpty(t, globalMissionFiles(t, env, ownedMission))
			home := os.Getenv("HOME")
			globalBefore := treeDigest(t, env.Global)

			var out Outcome
			var err error
			stderr := testhelpers.CaptureStderr(t, func() {
				out, err = runGlobalMissions(context.Background(), env)
			})
			require.NoError(t, err)
			assert.Empty(t, stderr, "the step logs; it never prints")
			assert.Equal(t, 1, out.Items)
			assert.False(t, out.Deferred)
			require.Len(t, out.Residuals, 1)
			assert.Equal(t, Residual{Step: "global-missions", ID: ownedMission, Reason: "imported; global copy kept"}, out.Residuals[0])
			assert.Contains(t, log.steps, "global-missions:import")

			s := mission.NewStoreWithRoots(env.Roots.Store, filepath.Dir(env.Global))
			c, err := s.Load(ownedMission)
			require.NoError(t, err)
			assert.Equal(t, ownedMission, c.MissionID)
			assert.FileExists(t, filepath.Join(repoMissionDir(env, ownedMission), "contract.yaml"))
			imported := readAll(t, repoMissionDir(env, ownedMission))
			assert.NotContains(t, imported, plantedToken, "the credential is redacted from the imported copy")
			assert.NotContains(t, imported, home, "the home path is redacted from the imported copy")
			assert.Equal(t, globalBefore, treeDigest(t, env.Global), "every global file is byte-identical: nothing is ever deleted")
			_, statErr := os.Stat(repoMissionDir(env, unownedMission))
			assert.True(t, os.IsNotExist(statErr), "a mission this repository does not own is not imported")

			repoBefore := treeDigest(t, env.Roots.Store)
			out, err = runGlobalMissions(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items, "second run changes nothing")
			assert.Empty(t, out.Residuals, "a noop import records no residual")
			assert.Equal(t, repoBefore, treeDigest(t, env.Roots.Store), "second run leaves the repository tree unchanged")
			assert.Equal(t, globalBefore, treeDigest(t, env.Global), "second run leaves the global tree unchanged")
		})
	}
}

// TestGlobalMissionsCorruptCopyIsNotAnImport: the repository copy is read
// back after the rename; a copy that does not match the redacted expectation
// is a distinct residual, not an import.
func TestGlobalMissionsCorruptCopyIsNotAnImport(t *testing.T) {
	env, _ := corpusEnvLogged(t, "v3.11.0")
	ownMission(t, env, ownedMission)
	globalBefore := treeDigest(t, env.Global)
	restore := mission.SetImportVerifyHook(func(dir string) {
		require.NoError(t, os.Truncate(filepath.Join(dir, "results.yaml"), 3))
	})
	t.Cleanup(restore)

	out, err := runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items, "a copy that failed verification is not an import")
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, ownedMission, out.Residuals[0].ID)
	assert.Contains(t, out.Residuals[0].Reason, "results.yaml")
	assert.NotEqual(t, "imported; global copy kept", out.Residuals[0].Reason)
	assert.Equal(t, globalBefore, treeDigest(t, env.Global), "the global files remain")
	_, statErr := os.Stat(repoMissionDir(env, ownedMission))
	assert.True(t, os.IsNotExist(statErr), "the unverified copy is removed so it cannot shadow the global one")

	// Run 2 with the fault still present retries the copy and fails the same
	// way; the corrupt bytes are never relabelled as a legitimate divergence.
	out, err = runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	require.Len(t, out.Residuals, 1)
	assert.Contains(t, out.Residuals[0].Reason, "results.yaml")
	assert.NotEqual(t, mission.ReasonRepoCopyDiverged, out.Residuals[0].Reason)
	assert.Equal(t, globalBefore, treeDigest(t, env.Global))

	// With the fault gone the next run imports cleanly.
	restore()
	out, err = runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Items, "the retry succeeds once the copy reads back as written")
	assert.FileExists(t, filepath.Join(repoMissionDir(env, ownedMission), "contract.yaml"))
}

// TestGlobalMissionsDivergedRepositoryCopy: a repository copy that was
// updated after the import differs from the redacted global expectation for
// a legitimate reason; the step says so with its own reason, not the
// corruption one, and nothing changes.
func TestGlobalMissionsDivergedRepositoryCopy(t *testing.T) {
	env, _ := corpusEnvLogged(t, "v3.11.0")
	ownMission(t, env, ownedMission)
	out, err := runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, 1, out.Items)
	cp := filepath.Join(repoMissionDir(env, ownedMission), "contract.yaml")
	b, err := os.ReadFile(cp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cp, append(b, []byte("# later repository edit\n")...), 0o600))
	globalBefore, repoBefore := treeDigest(t, env.Global), treeDigest(t, env.Roots.Store)

	out, err = runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, Residual{Step: "global-missions", ID: ownedMission, Reason: "repository copy has diverged; global copy kept"}, out.Residuals[0])
	assert.Equal(t, globalBefore, treeDigest(t, env.Global))
	assert.Equal(t, repoBefore, treeDigest(t, env.Roots.Store))
}

func TestGlobalMissionsNothingGlobal(t *testing.T) {
	for _, v := range []string{"v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4", "v4.21.0", "v4.22.1"} {
		t.Run(v, func(t *testing.T) {
			env, _ := corpusEnvLogged(t, v)
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
	env, log := corpusEnvLogged(t, "v3.11.0")
	ownMission(t, env, ownedMission)
	ownMission(t, env, unownedMission)
	// A torn log line cannot be redacted, so mission 001 cannot be imported.
	require.NoError(t, os.WriteFile(filepath.Join(env.Global, "missions", ownedMission+".jsonl"), []byte(`{"event":"clo`+"\n"), 0o600))
	// A torn line in the repository's own audit is a warning the step logs.
	live := audit.LiveAuditPath(env.Roots.Store, "sess-torn")
	require.NoError(t, os.WriteFile(live, []byte("{\"contract_id\":\n"), 0o600))
	globalBefore := treeDigest(t, env.Global)

	var out Outcome
	var err error
	stderr := testhelpers.CaptureStderr(t, func() {
		out, err = runGlobalMissions(context.Background(), env)
	})
	require.NoError(t, err, "the step finishes")
	assert.Empty(t, stderr, "a malformed audit line is logged, not printed")
	assert.Contains(t, log.steps, "global-missions:scan-warning")
	assert.Equal(t, 1, out.Items, "the other owned mission is still imported")
	require.Len(t, out.Residuals, 2)
	var reasons []string
	for _, r := range out.Residuals {
		assert.Equal(t, "global-missions", r.Step)
		assert.NotContains(t, r.Reason, "ethos mission migrate")
		reasons = append(reasons, r.ID+": "+r.Reason)
	}
	assert.Contains(t, reasons, unownedMission+": imported; global copy kept")
	assert.Equal(t, globalBefore, treeDigest(t, env.Global), "nothing global is deleted")
}

func TestGlobalMissionsOutsideRepository(t *testing.T) {
	env, _ := corpusEnvLogged(t, "v3.11.0")
	env.Roots.Store = ""
	out, err := runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	assert.NotEmpty(t, globalMissionFiles(t, env, ownedMission))
}
