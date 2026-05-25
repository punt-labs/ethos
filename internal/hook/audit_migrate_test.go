package hook

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLegacyAudit drops a legacy <id>.audit.jsonl file under
// globalDir containing entries. Used by every migrate test to stage
// the v3.11 starting state.
func writeLegacyAudit(t *testing.T, globalDir, sessionID string, entries []auditEntry) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	path := filepath.Join(globalDir, sessionID+".audit.jsonl")
	for _, e := range entries {
		require.NoError(t, writeAuditEntry(path, e))
	}
	return path
}

// repoSessionDir creates <repoRoot>/.ethos/sessions/<date>-<id> and
// returns the absolute path. Used by tests that need the matching
// repo-tree session directory to exist before migrate runs.
func repoSessionDir(t *testing.T, repoRoot, date, sessionID string) string {
	t.Helper()
	dir := filepath.Join(repoRoot, ".punt-labs", "ethos", "sessions", date+"-"+sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return dir
}

// readRepoAuditEntries reads
// <repoRoot>/.ethos/sessions/<date>-<id>/audit.jsonl. Returns nil
// entries when the file does not exist.
func readRepoAuditEntries(t *testing.T, sessionDir string) []auditEntry {
	t.Helper()
	entries, err := readAuditEntries(filepath.Join(sessionDir, "audit.jsonl"))
	require.NoError(t, err)
	return entries
}

func sampleEntry(session, ts, tool, hash string) auditEntry {
	return auditEntry{
		Ts:               ts,
		Session:          session,
		Tool:             tool,
		ToolInputHash:    hash,
		ToolInputPreview: "preview-" + hash,
	}
}

func TestMigrateAudit_NoLegacyFiles(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(globalDir, 0o700))

	var out bytes.Buffer
	err := MigrateAudit(globalDir, repoRoot, false, &out)
	require.NoError(t, err)
	assert.Equal(t, "nothing to migrate\n", out.String())
}

func TestMigrateAudit_NoGlobalDir(t *testing.T) {
	// Missing global dir is treated the same as empty: no error,
	// "nothing to migrate".
	globalDir := filepath.Join(t.TempDir(), "does-not-exist")
	repoRoot := t.TempDir()

	var out bytes.Buffer
	err := MigrateAudit(globalDir, repoRoot, false, &out)
	require.NoError(t, err)
	assert.Equal(t, "nothing to migrate\n", out.String())
}

func TestMigrateAudit_CopiesEntriesAndDeletesLegacy(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()

	sessID := "sess-abc"
	legacyEntries := []auditEntry{
		sampleEntry(sessID, "2026-05-22T10:00:00Z", "Bash", "h1"),
		sampleEntry(sessID, "2026-05-22T10:00:01Z", "Read", "h2"),
	}
	legacyPath := writeLegacyAudit(t, globalDir, sessID, legacyEntries)
	repoDir := repoSessionDir(t, repoRoot, "2026-05-22", sessID)

	var out bytes.Buffer
	err := MigrateAudit(globalDir, repoRoot, false, &out)
	require.NoError(t, err)

	// Legacy gone.
	_, statErr := os.Stat(legacyPath)
	assert.True(t, os.IsNotExist(statErr), "legacy file should be deleted: %v", statErr)

	// Repo file has both entries in order.
	got := readRepoAuditEntries(t, repoDir)
	require.Len(t, got, 2)
	require.NoError(t, auditEntriesEqual(legacyEntries, got))

	assert.Contains(t, out.String(), "migrate sess-abc")
	assert.Contains(t, out.String(), "2 new entries")
}

func TestMigrateAudit_IdempotentNoDoubleWrite(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()

	sessID := "sess-dup"
	entries := []auditEntry{
		sampleEntry(sessID, "2026-05-22T10:00:00Z", "Bash", "h1"),
		sampleEntry(sessID, "2026-05-22T10:00:01Z", "Read", "h2"),
	}
	writeLegacyAudit(t, globalDir, sessID, entries)
	repoDir := repoSessionDir(t, repoRoot, "2026-05-22", sessID)

	// First run: copies.
	var out1 bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out1))

	// Stage the legacy file again to simulate a second machine or a
	// stale tree, then run again. Repo already has the entries; the
	// dedupe must skip them and not double-write.
	writeLegacyAudit(t, globalDir, sessID, entries)

	var out2 bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out2))

	got := readRepoAuditEntries(t, repoDir)
	require.Len(t, got, 2, "second run must not duplicate entries")

	assert.Contains(t, out2.String(), "noop sess-dup")
}

func TestMigrateAudit_CrossRepoSessionLeftAlone(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()

	// Legacy session id has no matching repo-tree dir — belongs to a
	// different repo. The legacy file must stay in place.
	otherID := "sess-otherrepo"
	legacyPath := writeLegacyAudit(t, globalDir, otherID, []auditEntry{
		sampleEntry(otherID, "2026-05-22T11:00:00Z", "Bash", "x"),
	})

	var out bytes.Buffer
	err := MigrateAudit(globalDir, repoRoot, false, &out)
	require.NoError(t, err)

	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr, "legacy file must survive cross-repo migrate")

	assert.Contains(t, out.String(), "skip sess-otherrepo")
	assert.Contains(t, out.String(), "no repo-tree session")
}

func TestMigrateAudit_DryRun(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()

	sessID := "sess-dry"
	legacyEntries := []auditEntry{
		sampleEntry(sessID, "2026-05-22T10:00:00Z", "Bash", "h1"),
	}
	legacyPath := writeLegacyAudit(t, globalDir, sessID, legacyEntries)
	repoDir := repoSessionDir(t, repoRoot, "2026-05-22", sessID)

	var out bytes.Buffer
	err := MigrateAudit(globalDir, repoRoot, true, &out)
	require.NoError(t, err)

	// Legacy file still present.
	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr, "dry-run must not delete legacy file")

	// Repo audit file not created.
	_, statErr = os.Stat(filepath.Join(repoDir, "audit.jsonl"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not write repo file")

	assert.Contains(t, out.String(), "dry-run")
	assert.Contains(t, out.String(), "1 new entries")
}

func TestMigrateAudit_PartialEntriesAlreadyInRepo(t *testing.T) {
	// Repo already has entry h1; legacy has h1 + h2. Only h2 should
	// be appended. The repo file ends with [h1, h2].
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()

	sessID := "sess-partial"
	repoDir := repoSessionDir(t, repoRoot, "2026-05-22", sessID)
	repoFile := filepath.Join(repoDir, "audit.jsonl")

	repoSeed := sampleEntry(sessID, "2026-05-22T10:00:00Z", "Bash", "h1")
	require.NoError(t, writeAuditEntry(repoFile, repoSeed))

	legacyEntries := []auditEntry{
		sampleEntry(sessID, "2026-05-22T10:00:00Z", "Bash", "h1"),
		sampleEntry(sessID, "2026-05-22T10:00:01Z", "Read", "h2"),
	}
	writeLegacyAudit(t, globalDir, sessID, legacyEntries)

	var out bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out))

	got := readRepoAuditEntries(t, repoDir)
	require.Len(t, got, 2, "h1 must not be duplicated")
	assert.Equal(t, "h1", got[0].ToolInputHash)
	assert.Equal(t, "h2", got[1].ToolInputHash)

	assert.Contains(t, out.String(), "1 new entries")
}

func TestMigrateAudit_ReadOnlyLegacyFileSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses unix mode checks")
	}
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()

	sessID := "sess-ro"
	legacyPath := writeLegacyAudit(t, globalDir, sessID, []auditEntry{
		sampleEntry(sessID, "2026-05-22T10:00:00Z", "Bash", "h1"),
	})
	repoSessionDir(t, repoRoot, "2026-05-22", sessID)

	// Strip read on the legacy file so readAuditEntries returns
	// ErrPermission.
	require.NoError(t, os.Chmod(legacyPath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(legacyPath, 0o600) })

	var out bytes.Buffer
	err := MigrateAudit(globalDir, repoRoot, false, &out)
	require.NoError(t, err)

	// Legacy file still present (no destructive action on a
	// read-only source).
	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr)

	assert.Contains(t, out.String(), "skip sess-ro")
	assert.Contains(t, out.String(), "read-only")
}

func TestMigrateAudit_EmptyRepoRoot(t *testing.T) {
	// repoRoot="" is a hard error: the caller (cmd/ethos) is
	// expected to surface "must run inside a repo" before reaching
	// the library. Defense in depth.
	var out bytes.Buffer
	err := MigrateAudit(t.TempDir(), "", false, &out)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "repoRoot"))
}

func TestMigrateAudit_LegacyFileEmptied(t *testing.T) {
	// An empty legacy file (zero entries) should be removed without
	// writing anything to the repo tree. Output is the "noop"
	// decision so an operator running -v sees that the file was
	// processed.
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()

	sessID := "sess-empty"
	legacyPath := filepath.Join(globalDir, sessID+".audit.jsonl")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	require.NoError(t, os.WriteFile(legacyPath, nil, 0o600))
	repoSessionDir(t, repoRoot, "2026-05-22", sessID)

	var out bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out))

	_, statErr := os.Stat(legacyPath)
	assert.True(t, os.IsNotExist(statErr), "empty legacy file should be cleaned up")
	assert.Contains(t, out.String(), "noop sess-empty")
}
