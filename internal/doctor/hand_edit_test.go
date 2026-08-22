package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// missionArtifactDir returns <storeRoot>/.punt-labs/ethos/missions/<id>,
// creating it. Test helper shared by every case in this file.
func missionArtifactDir(t *testing.T, storeRoot, missionID string) string {
	t.Helper()
	dir := filepath.Join(storeRoot, ".punt-labs", "ethos", "missions", missionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return dir
}

func TestCheckNoHandEditedMissionFiles_NoStoreRoot(t *testing.T) {
	r := CheckNoHandEditedMissionFiles("")
	assert.Equal(t, "PASS", r.Status)
	assert.Equal(t, "not in a repo", r.Detail)
}

func TestCheckNoHandEditedMissionFiles_NoMissionsDir(t *testing.T) {
	r := CheckNoHandEditedMissionFiles(t.TempDir())
	assert.Equal(t, "PASS", r.Status)
	assert.Equal(t, "no missions directory", r.Detail)
}

func TestCheckNoHandEditedMissionFiles_CleanFilesPass(t *testing.T) {
	root := t.TempDir()
	dir := missionArtifactDir(t, root, "m-2026-08-22-001")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "contract.yaml"),
		[]byte("mission_id: m-2026-08-22-001\nstatus: closed\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "results.yaml"),
		[]byte("results:\n  - mission: m-2026-08-22-001\n    round: 1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "reflections.yaml"),
		[]byte("reflections: []\n"), 0o600))

	r := CheckNoHandEditedMissionFiles(root)
	assert.Equal(t, "PASS", r.Status)
	assert.Equal(t, "no hand-edited mission files", r.Detail)
}

// TestCheckNoHandEditedMissionFiles is the ethos-ecpv regression
// gate: a hand-appended "# CORRECTED:" comment on a results.yaml is
// exactly what happened when no sanctioned correction mechanism
// existed. The check must flag it and name the file and point at the
// sanctioned fix. Named to match DES-072's Verification section
// exactly; the sibling _-suffixed tests in this file cover the
// individual edge cases this one bare name can't hold by itself.
func TestCheckNoHandEditedMissionFiles(t *testing.T) {
	root := t.TempDir()
	dir := missionArtifactDir(t, root, "m-2026-08-22-002")
	body := "results:\n  - mission: m-2026-08-22-002\n    round: 1\n" +
		"# CORRECTED: make check failure was a stale worktree, not pre-existing\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "results.yaml"), []byte(body), 0o600))

	r := CheckNoHandEditedMissionFiles(root)
	assert.Equal(t, "FAIL", r.Status)
	assert.Contains(t, r.Detail, "m-2026-08-22-002/results.yaml")
	assert.Contains(t, r.Detail, "ethos mission correct")
}

// TestCheckNoHandEditedMissionFiles_IgnoresBlockScalarHash is the
// false-positive regression gate: a legitimate multi-line prose block
// scalar whose content starts a line with '#' (e.g. referencing a PR
// number) must NOT be flagged — YAML's block-scalar grammar does not
// treat '#' inside literal content as a comment marker, and this
// repo's own tracked mission data has exactly this shape.
func TestCheckNoHandEditedMissionFiles_IgnoresBlockScalarHash(t *testing.T) {
	root := t.TempDir()
	dir := missionArtifactDir(t, root, "m-2026-08-22-003")
	body := `results:
  - mission: m-2026-08-22-003
    round: 1
    prose: |
      PR #437 restored dev-plugin-only state per #430: plugin.json back
      to ethos-dev.
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "results.yaml"), []byte(body), 0o600))

	r := CheckNoHandEditedMissionFiles(root)
	assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
}

// TestCheckNoHandEditedMissionFiles_IgnoresLogJSONL asserts the check
// scans only the three closed-set YAML filenames — log.jsonl and its
// sealed chunks are machine-appended JSONL and out of scope, even
// though a corrupt or hand-edited log line has its own detection path
// elsewhere (LoadEvents' warnings).
func TestCheckNoHandEditedMissionFiles_IgnoresLogJSONL(t *testing.T) {
	root := t.TempDir()
	dir := missionArtifactDir(t, root, "m-2026-08-22-004")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "log.jsonl"),
		[]byte("# not a real jsonl line, but log.jsonl is out of scope\n"), 0o600))

	r := CheckNoHandEditedMissionFiles(root)
	assert.Equal(t, "PASS", r.Status)
}

// TestCheckNoHandEditedMissionFiles_EmptyFilesPass asserts a
// freshly-created, zero-byte results.yaml (before any result is ever
// appended) does not trip the detector.
func TestCheckNoHandEditedMissionFiles_EmptyFilesPass(t *testing.T) {
	root := t.TempDir()
	dir := missionArtifactDir(t, root, "m-2026-08-22-005")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "results.yaml"), []byte(""), 0o600))

	r := CheckNoHandEditedMissionFiles(root)
	assert.Equal(t, "PASS", r.Status)
}

// TestCheckNoHandEditedMissionFiles_MultipleFlagged asserts every
// offending file is named, not just the first one found.
func TestCheckNoHandEditedMissionFiles_MultipleFlagged(t *testing.T) {
	root := t.TempDir()
	dir1 := missionArtifactDir(t, root, "m-2026-08-22-006")
	dir2 := missionArtifactDir(t, root, "m-2026-08-22-007")
	require.NoError(t, os.WriteFile(filepath.Join(dir1, "contract.yaml"),
		[]byte("mission_id: m-2026-08-22-006\n# hand-edited\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "reflections.yaml"),
		[]byte("reflections: []\n# hand-edited\n"), 0o600))

	r := CheckNoHandEditedMissionFiles(root)
	assert.Equal(t, "FAIL", r.Status)
	assert.Contains(t, r.Detail, "m-2026-08-22-006/contract.yaml")
	assert.Contains(t, r.Detail, "m-2026-08-22-007/reflections.yaml")
}
