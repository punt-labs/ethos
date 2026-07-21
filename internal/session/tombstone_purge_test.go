package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeUnsealedLive writes an unsealed live audit line under repo for sessionID.
func writeUnsealedLive(t *testing.T, repo, sessionID string) {
	t.Helper()
	live := audit.LiveAuditPath(repo, sessionID)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	line := `{"ts":"` + audit.FormatLineTS(100) + `","session":"` + sessionID + `","tool":"Read"}` + "\n"
	require.NoError(t, os.WriteFile(live, []byte(line), 0o600))
}

func TestPurgeTombstoned_RefusesUnsealed(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	require.NoError(t, s.Create("sess-unsealed", root, primary, repo, ""))
	writeUnsealedLive(t, repo, "sess-unsealed")

	purged, refused, err := s.PurgeTombstoned(false)
	require.NoError(t, err)
	assert.Empty(t, purged)
	assert.Contains(t, refused, "sess-unsealed")

	// The session survives — a refused purge does not delete it.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Contains(t, ids, "sess-unsealed")
}

func TestPurgeTombstoned_ForceLeavesTombstone(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.Create("sess-forced", root, primary, repo, ""))
	writeUnsealedLive(t, repo, "sess-forced")

	purged, refused, err := s.PurgeTombstoned(true)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-forced")
	assert.Empty(t, refused)

	// A flagged tombstone records the loss.
	tb, err := audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-forced.purged"))
	require.NoError(t, err)
	assert.True(t, tb.UnsealedLines)
	assert.Equal(t, repo, tb.Repo)
}

func TestPurgeTombstoned_CleanSessionNoTombstone(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.Create("sess-clean", root, primary, repo, ""))
	// No live file at all → nothing unsealed, but the recorded live file is
	// "gone", so a tombstone with LiveFileGone is written (a checkout that
	// never wrote, or was deleted). Purge still proceeds.
	purged, refused, err := s.PurgeTombstoned(false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-clean")
	assert.Empty(t, refused)
}
