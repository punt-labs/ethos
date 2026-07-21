package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/audit"
	"github.com/punt-labs/ethos/internal/mission"
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

// sealMissionChunkFor writes a tracked mission chunk carrying sessionID (so the
// session is provably expected to have a mission live log there).
func sealMissionChunkFor(t *testing.T, repo, missionID, sessionID string) {
	t.Helper()
	dir := audit.SealedMissionDir(repo, missionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	name := audit.MissionChunkFile(sessionID, 100, 200)
	body := `{"ts":"` + audit.FormatLineTS(100) + `","event":"create","actor":"c"}` + "\n" +
		`{"ts":"` + audit.FormatLineTS(200) + `","event":"close","actor":"c"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}

func TestPurgeTombstoned_LostMissionLiveFlagsTombstone(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	require.NoError(t, s.Create("sess-ml", root, primary, repo, ""))
	// The session sealed a mission chunk but its mission live log is gone
	// (never on disk) — REQ-1: purge must flag this loss in a tombstone.
	sealMissionChunkFor(t, repo, "m-2026-07-21-009", "sess-ml")

	purged, refused, err := s.PurgeTombstoned(false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-ml")
	assert.Empty(t, refused)

	tb, err := audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-ml.purged"))
	require.NoError(t, err)
	assert.True(t, tb.LiveFileGone, "lost mission live log must set the tombstone flag")
}

func TestPurgeTombstoned_ClaimedButUnsealedMissionFlagsTombstone(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	require.NoError(t, s.Create("sess-claimed", root, primary, repo, ""))
	// REQ-1 residual: the session CLAIMED a mission (sidecar binding) but sealed
	// no chunk, and its mission live log is gone. With only chunk-derived
	// enumeration this loss is silent; the mission-binding union must catch it.
	require.NoError(t, mission.WriteActiveMission(s.root, "sess-claimed", "m-2026-07-21-009"))

	purged, refused, err := s.PurgeTombstoned(false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-claimed")
	assert.Empty(t, refused)

	tb, err := audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-claimed.purged"))
	require.NoError(t, err)
	assert.True(t, tb.LiveFileGone, "claimed-but-unsealed lost mission live log must set the tombstone flag")
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
