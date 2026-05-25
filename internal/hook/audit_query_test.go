package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeRepoTreeSession stages a repo-tree session directory at
// <repoRoot>/.ethos/sessions/<date>-<id>/audit.jsonl with the given
// entries, in order.
func writeRepoTreeSession(t *testing.T, repoRoot, date, sessionID string, entries []auditEntry) string {
	t.Helper()
	dir := filepath.Join(repoRoot, ".punt-labs", "ethos", "sessions", date+"-"+sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "audit.jsonl")
	for _, e := range entries {
		require.NoError(t, writeAuditEntry(path, e))
	}
	return path
}

// writeLegacySession stages a legacy global audit file at
// <globalDir>/<sessionID>.audit.jsonl with the given entries.
func writeLegacySession(t *testing.T, globalDir, sessionID string, entries []auditEntry) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	path := filepath.Join(globalDir, sessionID+".audit.jsonl")
	for _, e := range entries {
		require.NoError(t, writeAuditEntry(path, e))
	}
	return path
}

// queryEntry is a fixture helper.
func queryEntry(session, ts, tool, hash, delegation string) auditEntry {
	return auditEntry{
		Ts:            ts,
		Session:       session,
		Tool:          tool,
		ToolInputHash: hash,
		DelegationID:  delegation,
	}
}

func TestQueryAuditByDelegation_SingleRepoSessionMatches(t *testing.T) {
	repo := t.TempDir()
	globalDir := filepath.Join(t.TempDir(), "sessions")

	writeRepoTreeSession(t, repo, "2026-05-22", "sess-a", []auditEntry{
		queryEntry("sess-a", "2026-05-22T10:00:00Z", "Bash", "h1", "d-007"),
		queryEntry("sess-a", "2026-05-22T10:00:01Z", "Read", "h2", "d-007"),
		queryEntry("sess-a", "2026-05-22T10:00:02Z", "Write", "h3", "d-008"),
	})

	got, err := QueryAuditByDelegation(repo, globalDir, "d-007")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "Bash", got[0].Tool)
	assert.Equal(t, "Read", got[1].Tool)
}

func TestQueryAuditByDelegation_MultipleRepoSessions(t *testing.T) {
	repo := t.TempDir()
	globalDir := filepath.Join(t.TempDir(), "sessions")

	writeRepoTreeSession(t, repo, "2026-05-21", "sess-a", []auditEntry{
		queryEntry("sess-a", "2026-05-21T09:00:00Z", "Bash", "h1", "d-100"),
		queryEntry("sess-a", "2026-05-21T09:00:01Z", "Read", "h2", "d-200"),
	})
	writeRepoTreeSession(t, repo, "2026-05-22", "sess-b", []auditEntry{
		queryEntry("sess-b", "2026-05-22T11:00:00Z", "Write", "h3", "d-100"),
	})
	writeRepoTreeSession(t, repo, "2026-05-22", "sess-c", []auditEntry{
		queryEntry("sess-c", "2026-05-22T12:00:00Z", "Edit", "h4", "d-300"),
	})

	got, err := QueryAuditByDelegation(repo, globalDir, "d-100")
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Lexicographic over directory names puts sess-a before sess-b.
	assert.Equal(t, "Bash", got[0].Tool)
	assert.Equal(t, "Write", got[1].Tool)
}

func TestQueryAuditByDelegation_LegacyFallback(t *testing.T) {
	repo := t.TempDir()
	globalDir := filepath.Join(t.TempDir(), "sessions")

	// Repo tree exists but has no matching session.
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos", "sessions"), 0o700))
	writeLegacySession(t, globalDir, "sess-legacy", []auditEntry{
		queryEntry("sess-legacy", "2026-05-20T08:00:00Z", "Bash", "h1", "d-legacy"),
		queryEntry("sess-legacy", "2026-05-20T08:00:01Z", "Read", "h2", "d-other"),
	})

	got, err := QueryAuditByDelegation(repo, globalDir, "d-legacy")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Bash", got[0].Tool)
	assert.Equal(t, "sess-legacy", got[0].Session)
}

// TestQueryAuditByDelegation_LegacyAlsoScannedWhenRepoSessionExists
// asserts that the query walker reads BOTH repo-tree AND legacy for
// the same session ID. During a migration window, partial entries
// can live in either place; the operator runs `ethos audit migrate`
// to consolidate. Skipping legacy by session-id presence would hide
// matching entries that hadn't yet been merged (Bugbot MED on PR
// #328: prior behavior skipped legacy as soon as a repo dir existed,
// which masked entries the migration hadn't yet copied over).
func TestQueryAuditByDelegation_LegacyAlsoScannedWhenRepoSessionExists(t *testing.T) {
	repo := t.TempDir()
	globalDir := filepath.Join(t.TempDir(), "sessions")

	// Same session id has both a repo-tree dir and a legacy file with
	// different entries (mid-migration state). Both must surface.
	writeRepoTreeSession(t, repo, "2026-05-22", "sess-dup", []auditEntry{
		queryEntry("sess-dup", "2026-05-22T10:00:00Z", "Bash", "h-repo", "d-007"),
	})
	writeLegacySession(t, globalDir, "sess-dup", []auditEntry{
		queryEntry("sess-dup", "2026-05-22T09:00:00Z", "Bash", "h-legacy", "d-007"),
	})

	got, err := QueryAuditByDelegation(repo, globalDir, "d-007")
	require.NoError(t, err)
	require.Len(t, got, 2,
		"both repo-tree and legacy entries must surface during a migration window")
}

func TestQueryAuditByDelegation_NoMatches(t *testing.T) {
	repo := t.TempDir()
	globalDir := filepath.Join(t.TempDir(), "sessions")

	writeRepoTreeSession(t, repo, "2026-05-22", "sess-a", []auditEntry{
		queryEntry("sess-a", "2026-05-22T10:00:00Z", "Bash", "h1", "d-008"),
	})
	writeLegacySession(t, globalDir, "sess-b", []auditEntry{
		queryEntry("sess-b", "2026-05-22T11:00:00Z", "Read", "h2", "d-009"),
	})

	got, err := QueryAuditByDelegation(repo, globalDir, "d-missing")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestQueryAuditByDelegation_EmptyDelegationID(t *testing.T) {
	repo := t.TempDir()
	globalDir := filepath.Join(t.TempDir(), "sessions")

	writeRepoTreeSession(t, repo, "2026-05-22", "sess-a", []auditEntry{
		queryEntry("sess-a", "2026-05-22T10:00:00Z", "Bash", "h1", "d-007"),
	})

	got, err := QueryAuditByDelegation(repo, globalDir, "")
	require.NoError(t, err)
	assert.Empty(t, got, "empty delegationID must not match anything")
}

func TestQueryAuditByDelegation_MissingDirs(t *testing.T) {
	// Neither repo nor global dir exist. Returns empty, nil.
	repo := filepath.Join(t.TempDir(), "absent-repo")
	globalDir := filepath.Join(t.TempDir(), "absent-global")

	got, err := QueryAuditByDelegation(repo, globalDir, "d-007")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestQueryAuditByDelegation_RepoEmptyLegacyMatches(t *testing.T) {
	// Repo root unset: skip repo walk entirely, only consult legacy.
	globalDir := filepath.Join(t.TempDir(), "sessions")
	writeLegacySession(t, globalDir, "sess-only", []auditEntry{
		queryEntry("sess-only", "2026-05-22T10:00:00Z", "Bash", "h1", "d-007"),
	})

	got, err := QueryAuditByDelegation("", globalDir, "d-007")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "sess-only", got[0].Session)
}

func TestSessionIDFromDir(t *testing.T) {
	cases := []struct {
		name, want string
	}{
		{"2026-05-22-sess-abc", "sess-abc"},
		{"2026-05-22-x", "x"},
		{"not-a-session", ""},
		{"2026-05-22", ""},
		{"2026-05-22-", ""},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sessionIDFromDir(c.name)
			assert.Equal(t, c.want, got)
		})
	}
}
