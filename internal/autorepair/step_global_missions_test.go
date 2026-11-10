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
// signal the repository keeps for a mission it created. The session is
// given a global roster whose checkout is this repository, which makes the
// ownership exclusive.
func ownMission(t *testing.T, env Env, id string) {
	t.Helper()
	ownMissionFrom(t, env, "sess-owner", id, env.Roots.Checkout)
}

// ownMissionFrom is ownMission with the session and the checkout its global
// roster names; an empty checkout writes a roster with no checkout field,
// as every roster before v4.13.0 was.
func ownMissionFrom(t *testing.T, env Env, session, id, checkout string) {
	t.Helper()
	path := audit.LiveAuditPath(env.Roots.Store, session)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"ts":"2026-11-09T23:02:12Z","session":"` + session + `","tool":"Bash","contract_id":"` + id + `"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	roster := "session: " + session + "\nstarted: \"2026-11-09T23:02:11Z\"\n"
	if checkout != "" {
		roster += "checkout: " + checkout + "\n"
	}
	roster += "participants: []\n"
	require.NoError(t, os.MkdirAll(filepath.Join(env.Global, "sessions"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(env.Global, "sessions", session+".yaml"), []byte(roster), 0o600))
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

func TestGlobalMissions(t *testing.T) {
	for _, v := range []string{"v3.11.0", "v3.12.0"} {
		t.Run(v, func(t *testing.T) {
			env, log := corpusEnv(t, v)
			ownMission(t, env, ownedMission)
			plantSecrets(t, env, ownedMission)
			require.NotEmpty(t, globalMissionFiles(t, env, ownedMission))
			home := os.Getenv("HOME")

			var out Outcome
			var err error
			stderr := testhelpers.CaptureStderr(t, func() {
				out, err = runGlobalMissions(context.Background(), env)
			})
			require.NoError(t, err)
			assert.Empty(t, stderr, "the step logs; it never prints")
			assert.Equal(t, 1, out.Items)
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)
			assert.NotEmpty(t, log.steps, "the step logs what it moves")

			s := mission.NewStoreWithRoots(env.Roots.Store, filepath.Dir(env.Global))
			c, err := s.Load(ownedMission)
			require.NoError(t, err)
			assert.Equal(t, ownedMission, c.MissionID)
			assert.FileExists(t, filepath.Join(repoMissionDir(env, ownedMission), "contract.yaml"))
			imported := readAll(t, repoMissionDir(env, ownedMission))
			assert.NotContains(t, imported, plantedToken, "the credential is redacted from the imported copy")
			assert.NotContains(t, imported, home, "the home path is redacted from the imported copy")
			assert.Empty(t, globalMissionFiles(t, env, ownedMission), "global copy deleted once the verified redacted copy exists")
			assert.NotEmpty(t, globalMissionFiles(t, env, unownedMission), "a mission this repository does not own stays global")

			repoBefore, globalBefore := treeDigest(t, env.Roots.Store), treeDigest(t, env.Global)
			out, err = runGlobalMissions(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items, "second run changes nothing")
			assert.Equal(t, repoBefore, treeDigest(t, env.Roots.Store), "second run leaves the repository tree unchanged")
			assert.Equal(t, globalBefore, treeDigest(t, env.Global), "second run leaves the global tree unchanged")
		})
	}
}

// TestGlobalMissionsSharedOwnershipKeepsGlobal: a mission stamped by a
// session whose roster names another repository is imported as a copy, the
// global copy stays, and the step records why.
func TestGlobalMissionsSharedOwnershipKeepsGlobal(t *testing.T) {
	env, _ := corpusEnv(t, "v3.11.0")
	ownMission(t, env, ownedMission)
	ownMissionFrom(t, env, "sess-other", ownedMission, filepath.Join(t.TempDir(), "other-repo"))

	out, err := runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Items, "the copy is imported")
	assert.FileExists(t, filepath.Join(repoMissionDir(env, ownedMission), "contract.yaml"))
	assert.NotEmpty(t, globalMissionFiles(t, env, ownedMission), "the global copy is kept for the other repository")
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, Residual{Step: "global-missions", ID: ownedMission, Reason: "shared ownership; global copy kept"}, out.Residuals[0])

	out, err = runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	assert.NotEmpty(t, globalMissionFiles(t, env, ownedMission), "a second run does not delete it either")
}

// TestGlobalMissionsNoProofKeepsGlobal: a stamping session with no global
// roster, or a roster with no checkout (every roster before v4.13.0), gives
// no proof the ownership is exclusive. The copy is imported; the global copy
// stays.
func TestGlobalMissionsNoProofKeepsGlobal(t *testing.T) {
	for name, checkout := range map[string]string{"no-checkout-field": "", "no-roster": "none"} {
		t.Run(name, func(t *testing.T) {
			env, _ := corpusEnv(t, "v3.11.0")
			ownMissionFrom(t, env, "sess-owner", ownedMission, checkout)
			if checkout == "none" {
				require.NoError(t, os.Remove(filepath.Join(env.Global, "sessions", "sess-owner.yaml")))
			}
			out, err := runGlobalMissions(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 1, out.Items)
			assert.NotEmpty(t, globalMissionFiles(t, env, ownedMission))
			require.Len(t, out.Residuals, 1)
			assert.Equal(t, "shared ownership; global copy kept", out.Residuals[0].Reason)
		})
	}
}

// TestGlobalMissionsCorruptCopyIsNotDeleted: the repository copy is checked
// after the rename; a copy that does not match the redacted expectation
// leaves the global files in place and records a residual.
func TestGlobalMissionsCorruptCopyIsNotDeleted(t *testing.T) {
	env, _ := corpusEnv(t, "v3.11.0")
	ownMission(t, env, ownedMission)
	restore := mission.SetImportVerifyHook(func(dir string) {
		require.NoError(t, os.Truncate(filepath.Join(dir, "results.yaml"), 3))
	})
	defer restore()

	out, err := runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items, "a copy that failed verification is not an import")
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, ownedMission, out.Residuals[0].ID)
	assert.Contains(t, out.Residuals[0].Reason, "results.yaml")
	assert.NotEmpty(t, globalMissionFiles(t, env, ownedMission), "the global files remain")
}

// TestGlobalMissionsCrashBetweenDeletesIsRecoverable: the contract is
// deleted last, so a crash after the first delete leaves a candidate the
// next run finishes.
func TestGlobalMissionsCrashBetweenDeletesIsRecoverable(t *testing.T) {
	env, _ := corpusEnv(t, "v3.11.0")
	ownMission(t, env, ownedMission)
	deleted := 0
	restore := mission.SetImportDeleteHook(func(path string) {
		deleted++
		if deleted == 2 {
			panic("crash after the first delete")
		}
	})
	func() {
		defer func() { _ = recover() }()
		_, _ = runGlobalMissions(context.Background(), env)
	}()
	restore()
	remaining := globalMissionFiles(t, env, ownedMission)
	require.NotEmpty(t, remaining, "the crash left global files")
	assert.FileExists(t, filepath.Join(env.Global, "missions", ownedMission+".yaml"), "the contract is deleted last")

	out, err := runGlobalMissions(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, out.Residuals)
	assert.Empty(t, globalMissionFiles(t, env, ownedMission), "the second run cleans the rest")
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
	env, log := corpusEnv(t, "v3.11.0")
	ownMission(t, env, ownedMission)
	ownMission(t, env, unownedMission)
	// A torn log line cannot be redacted, so mission 001 cannot be imported.
	require.NoError(t, os.WriteFile(filepath.Join(env.Global, "missions", ownedMission+".jsonl"), []byte(`{"event":"clo`+"\n"), 0o600))
	// A torn line in the repository's own audit is a warning the step logs.
	live := audit.LiveAuditPath(env.Roots.Store, "sess-torn")
	require.NoError(t, os.WriteFile(live, []byte("{\"contract_id\":\n"), 0o600))

	var out Outcome
	var err error
	stderr := testhelpers.CaptureStderr(t, func() {
		out, err = runGlobalMissions(context.Background(), env)
	})
	require.NoError(t, err, "the step finishes")
	assert.Empty(t, stderr, "a malformed audit line is logged, not printed")
	assert.Contains(t, log.steps, "global-missions:scan-warning")
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
