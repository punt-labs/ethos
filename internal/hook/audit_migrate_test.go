package hook

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
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

// migrationSessionDir retains a legacy record-tree association while returning
// the new local destination. Migration never writes to the old record tree.
func migrationSessionDir(t *testing.T, root, date, session string) string {
	t.Helper()
	repoSessionDir(t, root, date, session)
	dir := archiveSessionDir(t, root, session, date)
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

func TestMigrateAudit_CopiesEntriesAndRetainsLegacy(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()

	sessID := "sess-abc"
	legacyEntries := []auditEntry{
		sampleEntry(sessID, "2026-05-22T10:00:00Z", "Bash", "h1"),
		sampleEntry(sessID, "2026-05-22T10:00:01Z", "Read", "h2"),
	}
	legacyPath := writeLegacyAudit(t, globalDir, sessID, legacyEntries)
	repoDir := migrationSessionDir(t, repoRoot, "2026-05-22", sessID)

	var out bytes.Buffer
	err := MigrateAudit(globalDir, repoRoot, false, &out)
	require.NoError(t, err)

	// Sources remain safe for older processes that already have them open.
	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr, "legacy source is retained")

	// Repo file has both entries in order.
	got := readRepoAuditEntries(t, repoDir)
	require.Len(t, got, 2)
	require.NoError(t, auditEntriesEqual(legacyEntries, got))

	assert.Contains(t, out.String(), "migrate sess-abc")
	assert.Contains(t, out.String(), "2 new entries")
}

// TestMigrateAudit_PreservesRawUnknownFields is SFH R2-4: migration must copy
// raw lines, so an audit_error sentinel and any unknown future field survive
// byte-intact. A decode-remarshal through auditEntry would strip both.
func TestMigrateAudit_PreservesRawUnknownFields(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessID := "sess-raw"

	sentinel := `{"ts":"2026-05-22T10:00:00Z","session":"sess-raw","audit_error":"fsync failed"}`
	future := `{"ts":"2026-05-22T10:00:01Z","session":"sess-raw","tool":"Bash","future_field":"keepme"}`
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	legacyPath := filepath.Join(globalDir, sessID+".audit.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte(sentinel+"\n"+future+"\n"), 0o600))
	repoDir := migrationSessionDir(t, repoRoot, "2026-05-22", sessID)

	var out bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out))

	data, err := os.ReadFile(filepath.Join(repoDir, "audit.jsonl"))
	require.NoError(t, err)
	body := string(data)
	assert.Contains(t, body, `"audit_error":"fsync failed"`, "sentinel must survive migration byte-intact")
	assert.Contains(t, body, `"future_field":"keepme"`, "unknown field must survive migration byte-intact")

	_, statErr := os.Stat(legacyPath)
	assert.NoError(t, statErr, "legacy source remains after migration")
}

// TestMigrateAudit_SameSecondSentinelsBothSurvive is SFH R3-1: two sentinels
// with the same second-precision ts but different audit_error content must both
// migrate — the hashless dedupe key falls back to the raw bytes, so distinct
// losses are not collapsed to one.
func TestMigrateAudit_SameSecondSentinelsBothSurvive(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessID := "sess-burst"

	s1 := `{"ts":"2026-05-22T10:00:00Z","session":"sess-burst","audit_error":"ENOSPC on chunk 1"}`
	s2 := `{"ts":"2026-05-22T10:00:00Z","session":"sess-burst","audit_error":"ENOSPC on chunk 2"}`
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	legacyPath := filepath.Join(globalDir, sessID+".audit.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte(s1+"\n"+s2+"\n"), 0o600))
	repoDir := migrationSessionDir(t, repoRoot, "2026-05-22", sessID)

	var out bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out))

	data, err := os.ReadFile(filepath.Join(repoDir, "audit.jsonl"))
	require.NoError(t, err)
	body := string(data)
	assert.Contains(t, body, "ENOSPC on chunk 1", "first same-second sentinel must survive")
	assert.Contains(t, body, "ENOSPC on chunk 2", "second same-second sentinel must not be deduped away")
}

// TestMigrateAudit_IdenticalSentinelBurstAllSurvive is SFH R4-1: an ENOSPC
// burst emits N byte-identical legacy sentinels (same second, same errno text).
// Set-dedupe would collapse them to one, silently under-reporting the loss;
// the multiset dedupe migrates all N.
func TestMigrateAudit_IdenticalSentinelBurstAllSurvive(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessID := "sess-enospc"

	s := `{"ts":"2026-05-22T10:00:00Z","session":"sess-enospc","audit_error":"write /x: no space left on device"}`
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	legacyPath := filepath.Join(globalDir, sessID+".audit.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte(s+"\n"+s+"\n"+s+"\n"), 0o600))
	repoDir := migrationSessionDir(t, repoRoot, "2026-05-22", sessID)

	var out bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out))

	got := readRepoAuditEntries(t, repoDir)
	assert.Len(t, got, 3, "all three identical loss markers must survive migration")
}

// TestMigrateAudit_IdenticalBurstResumesAfterCrash is SFH R4-1: a crash
// interrupts the append of a 3-line identical burst after 2 lines landed. The
// re-run must top the repo up to exactly 3 (append 1), and a further run must
// append 0 while retaining the legacy source.
func TestMigrateAudit_IdenticalBurstResumesAfterCrash(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessID := "sess-resume"

	s := `{"ts":"2026-05-22T10:00:00Z","session":"sess-resume","audit_error":"no space left on device"}`
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	legacyPath := filepath.Join(globalDir, sessID+".audit.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte(s+"\n"+s+"\n"+s+"\n"), 0o600))
	repoDir := migrationSessionDir(t, repoRoot, "2026-05-22", sessID)

	// Simulate a crash that landed only 2 of the 3 lines in the repo file.
	repoPath := filepath.Join(repoDir, "audit.jsonl")
	require.NoError(t, os.WriteFile(repoPath, []byte(s+"\n"+s+"\n"), 0o600))

	var out1 bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out1))
	require.Len(t, readRepoAuditEntries(t, repoDir), 3, "re-run must top up to exactly the legacy count")
	assert.Contains(t, out1.String(), "migrate sess-resume")

	// The top-up run installed a complete local collection without deleting source.
	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr, "legacy file remains after the top-up")

	// Re-stage the legacy burst (a stale tree or second machine) and run again.
	// The local archive already holds all three, so dedupe adds zero.
	require.NoError(t, os.WriteFile(legacyPath, []byte(s+"\n"+s+"\n"+s+"\n"), 0o600))
	var out2 bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out2))
	assert.Len(t, readRepoAuditEntries(t, repoDir), 3, "completed run must not double-write")
	assert.Contains(t, out2.String(), "noop sess-resume")
	_, statErr = os.Stat(legacyPath)
	assert.NoError(t, statErr, "legacy file remains after a completed re-run")
}

// A damaged retained destination is an explicit recovery error, not permission
// to overwrite evidence or silently treat it as an empty collection.
func TestMigrateAudit_TornDestinationRefusesWithoutChangingSource(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessID := "sess-torn"

	sentinel := `{"ts":"2026-05-22T10:00:01Z","session":"sess-torn","audit_error":"boom"}`
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, sessID+".audit.jsonl"), []byte(sentinel+"\n"), 0o600))
	repoDir := migrationSessionDir(t, repoRoot, "2026-05-22", sessID)
	// A prior write crashed mid-line, leaving a fragment with no terminator.
	repoPath := filepath.Join(repoDir, "audit.jsonl")
	torn := []byte(`{"ts":"2026-05-22T09:59:59Z","session":"sess-torn","tool":"Ba`)
	require.NoError(t, os.WriteFile(repoPath, torn, 0o600))

	var out bytes.Buffer
	err := MigrateAudit(globalDir, repoRoot, false, &out)
	require.ErrorContains(t, err, "legacy source retained")
	data, err := os.ReadFile(repoPath)
	require.NoError(t, err)
	assert.Equal(t, torn, data)
	data, err = os.ReadFile(filepath.Join(globalDir, sessID+".audit.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, sentinel+"\n", string(data))
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
	repoDir := migrationSessionDir(t, repoRoot, "2026-05-22", sessID)

	// First run: copies.
	var out1 bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out1))

	// The retained legacy file is unchanged on the second run.

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
	repoDir := migrationSessionDir(t, repoRoot, "2026-05-22", sessID)

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
	repoDir := migrationSessionDir(t, repoRoot, "2026-05-22", sessID)
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

func TestMigrateAudit_UnreadableLegacyFileRefuses(t *testing.T) {
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
	require.ErrorIs(t, err, os.ErrPermission)

	// Legacy file still present (no destructive action on a
	// read-only source).
	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr)

	assert.Contains(t, err.Error(), "legacy source retained")
}

func TestMigrateAudit_EmptyRepoRoot(t *testing.T) {
	// repoRoot="" is a hard error: the caller (cmd/ethos) is
	// expected to surface "must run inside a repo" before reaching
	// the library. Defense in depth.
	var out bytes.Buffer
	err := MigrateAudit(t.TempDir(), "", false, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repository root is empty")
}

func TestMigrateAudit_LegacyFileEmptied(t *testing.T) {
	// Empty sources are retained too; cleanup requires operator consent.
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
	assert.NoError(t, statErr, "empty legacy file remains")
	assert.Contains(t, out.String(), "0 new entries; source retained")
}
