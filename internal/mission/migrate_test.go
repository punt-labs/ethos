package mission

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyMissionFiles writes a contract + the named sibling artifacts
// under <globalRoot>/missions/. Each value names the file extension
// (".yaml", ".jsonl", ".results.yaml", ".reflections.yaml") mapped to
// the bytes to write. Returns the absolute legacy contract path.
func legacyMissionFiles(t *testing.T, globalRoot, missionID string, files map[string][]byte) string {
	t.Helper()
	dir := filepath.Join(globalRoot, "missions")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	for suffix, data := range files {
		path := filepath.Join(dir, missionID+suffix)
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	return filepath.Join(dir, missionID+".yaml")
}

// repoAuditWithMission stages an audit.jsonl line under
// <repoRoot>/.ethos/sessions/<date>-<id>/audit.jsonl referencing
// missionID as its contract_id. The presence of this file is what
// makes the mission a candidate for migration in this repo.
func repoAuditWithMission(t *testing.T, repoRoot, sessionID, missionID string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".ethos", "sessions", "2026-05-22-"+sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	line := `{"ts":"2026-05-22T10:00:00Z","session":"` + sessionID +
		`","tool":"Agent","contract_id":"` + missionID + `"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "audit.jsonl"), []byte(line), 0o600))
}

func TestMigrateMission_NothingToMigrate(t *testing.T) {
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()

	var out bytes.Buffer
	err := MigrateMission(globalRoot, repoRoot, "", false, &out)
	require.NoError(t, err)
	assert.Equal(t, "nothing to migrate\n", out.String())
}

func TestMigrateMission_NoLegacyMissionsDir(t *testing.T) {
	globalRoot := filepath.Join(t.TempDir(), "does-not-exist")
	repoRoot := t.TempDir()

	var out bytes.Buffer
	err := MigrateMission(globalRoot, repoRoot, "", false, &out)
	require.NoError(t, err)
	assert.Equal(t, "nothing to migrate\n", out.String())
}

func TestMigrateMission_CopiesAllArtifacts(t *testing.T) {
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-001"

	legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml":              []byte("mission_id: m-2026-05-22-001\nstatus: open\n"),
		".jsonl":             []byte(`{"type":"create","ts":"2026-05-22T10:00:00Z"}` + "\n"),
		".results.yaml":      []byte("- round: 1\n  verdict: pass\n"),
		".reflections.yaml":  []byte("- round: 1\n  recommendation: stop\n"),
	})
	repoAuditWithMission(t, repoRoot, "sess-1", id)

	var out bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, "", false, &out))

	// Repo tree has all four files.
	repoMissionDir := filepath.Join(repoRoot, ".ethos", "missions", id)
	for _, name := range []string{"contract.yaml", "log.jsonl", "results.yaml", "reflections.yaml"} {
		_, err := os.Stat(filepath.Join(repoMissionDir, name))
		require.NoErrorf(t, err, "expected %s in repo tree", name)
	}

	// Legacy files removed.
	for _, suffix := range []string{".yaml", ".jsonl", ".results.yaml", ".reflections.yaml"} {
		path := filepath.Join(globalRoot, "missions", id+suffix)
		_, err := os.Stat(path)
		require.Truef(t, os.IsNotExist(err), "expected legacy %s removed, stat err=%v", suffix, err)
	}

	assert.Contains(t, out.String(), "migrate "+id)
	assert.Contains(t, out.String(), ".ethos/missions/"+id)
}

func TestMigrateMission_OptionalSiblingsAbsent(t *testing.T) {
	// Only contract.yaml exists in the legacy tree. Migration must
	// still succeed; the optional sibling files are simply absent in
	// the repo tree.
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-002"

	legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml": []byte("mission_id: m-2026-05-22-002\n"),
	})
	repoAuditWithMission(t, repoRoot, "sess-2", id)

	var out bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, "", false, &out))

	repoMissionDir := filepath.Join(repoRoot, ".ethos", "missions", id)
	_, err := os.Stat(filepath.Join(repoMissionDir, "contract.yaml"))
	require.NoError(t, err)
	for _, name := range []string{"log.jsonl", "results.yaml", "reflections.yaml"} {
		_, err := os.Stat(filepath.Join(repoMissionDir, name))
		require.Truef(t, os.IsNotExist(err), "expected %s absent in repo tree", name)
	}
}

func TestMigrateMission_IdempotentReRun(t *testing.T) {
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-003"

	legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml": []byte("mission_id: m-2026-05-22-003\n"),
	})
	repoAuditWithMission(t, repoRoot, "sess-3", id)

	var out1 bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, "", false, &out1))
	assert.Contains(t, out1.String(), "migrate "+id)

	// Stage the legacy contract again to simulate a second invocation
	// reaching a half-state. The repo tree already has the contract;
	// the second run must be a no-op and leave the legacy file alone.
	legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml": []byte("mission_id: m-2026-05-22-003\n"),
	})

	var out2 bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, "", false, &out2))
	assert.Contains(t, out2.String(), "noop "+id)

	// Legacy file untouched by the noop.
	_, err := os.Stat(filepath.Join(globalRoot, "missions", id+".yaml"))
	require.NoError(t, err, "noop must not delete the legacy file")
}

func TestMigrateMission_CrossRepoMissionLeftAlone(t *testing.T) {
	// Legacy mission is not referenced by any audit entry in this
	// repo — belongs to a different repo's work tree. Must be left
	// in place.
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-004"

	legacyContract := legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml": []byte("mission_id: m-2026-05-22-004\n"),
	})

	var out bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, "", false, &out))

	_, err := os.Stat(legacyContract)
	require.NoError(t, err, "cross-repo mission must survive migrate")

	assert.Contains(t, out.String(), "skip "+id)
	assert.Contains(t, out.String(), "no repo session")
}

func TestMigrateMission_DryRunLeavesBothSidesIntact(t *testing.T) {
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-005"

	legacyContract := legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml":  []byte("mission_id: m-2026-05-22-005\n"),
		".jsonl": []byte(`{"type":"create"}` + "\n"),
	})
	repoAuditWithMission(t, repoRoot, "sess-5", id)

	var out bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, "", true, &out))

	// Legacy still present.
	_, err := os.Stat(legacyContract)
	require.NoError(t, err)
	// Repo-tree contract not created.
	_, err = os.Stat(filepath.Join(repoRoot, ".ethos", "missions", id, "contract.yaml"))
	require.True(t, os.IsNotExist(err), "dry-run must not write repo contract")

	assert.Contains(t, out.String(), "dry-run")
	assert.Contains(t, out.String(), "migrate "+id)
}

func TestMigrateMission_ExplicitMissionIDMissing(t *testing.T) {
	// Caller named a mission that does not exist on disk. Not an
	// error — the migrate command is idempotent and tolerates an
	// already-migrated or never-created ID.
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()

	var out bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, "m-2026-05-22-999", false, &out))
	assert.Contains(t, out.String(), "skip m-2026-05-22-999")
	assert.Contains(t, out.String(), "legacy contract missing")
}

func TestMigrateMission_ExplicitMissionIDIgnoresSiblings(t *testing.T) {
	// Caller names a mission ID. Only that mission is considered,
	// even though other missions are present in the legacy tree.
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	want := "m-2026-05-22-006"
	other := "m-2026-05-22-007"

	legacyMissionFiles(t, globalRoot, want, map[string][]byte{
		".yaml": []byte("mission_id: m-2026-05-22-006\n"),
	})
	legacyMissionFiles(t, globalRoot, other, map[string][]byte{
		".yaml": []byte("mission_id: m-2026-05-22-007\n"),
	})
	repoAuditWithMission(t, repoRoot, "sess-6", want)
	repoAuditWithMission(t, repoRoot, "sess-7", other)

	var out bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, want, false, &out))

	// Named mission migrated.
	_, err := os.Stat(filepath.Join(repoRoot, ".ethos", "missions", want, "contract.yaml"))
	require.NoError(t, err)
	// Other mission untouched in both trees.
	_, err = os.Stat(filepath.Join(globalRoot, "missions", other+".yaml"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(repoRoot, ".ethos", "missions", other, "contract.yaml"))
	require.True(t, os.IsNotExist(err))

	assert.Contains(t, out.String(), "migrate "+want)
	assert.NotContains(t, out.String(), other)
}

func TestMigrateMission_EmptyRepoRoot(t *testing.T) {
	var out bytes.Buffer
	err := MigrateMission(t.TempDir(), "", "", false, &out)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "repoRoot"))
}

func TestMigrateMission_EmptyGlobalRoot(t *testing.T) {
	var out bytes.Buffer
	err := MigrateMission("", t.TempDir(), "", false, &out)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "globalRoot"))
}

func TestMigrateMission_PartialFailureLeavesLegacyIntact(t *testing.T) {
	// If the repo-tree mission directory parent is unwritable, the
	// rename fails and both sources stay intact. Skip on root since
	// permissions are bypassed.
	if os.Geteuid() == 0 {
		t.Skip("root bypasses unix mode checks")
	}
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-008"

	legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml": []byte("mission_id: m-2026-05-22-008\n"),
	})
	repoAuditWithMission(t, repoRoot, "sess-8", id)

	// Lock the parent directory so MkdirTemp inside repoDir fails.
	missionsDir := filepath.Join(repoRoot, ".ethos", "missions")
	require.NoError(t, os.MkdirAll(missionsDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(missionsDir, 0o700) })

	var out bytes.Buffer
	err := MigrateMission(globalRoot, repoRoot, "", false, &out)
	require.Error(t, err)

	// Legacy still present.
	_, statErr := os.Stat(filepath.Join(globalRoot, "missions", id+".yaml"))
	require.NoError(t, statErr, "legacy contract must survive failed migrate")
}
