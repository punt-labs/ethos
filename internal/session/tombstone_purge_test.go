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

// breakMissionTree makes the repo's mission tree unreadable by planting a
// regular file where the missions directory must be, so the guard's mission
// probes (SessionBoundMissions / ExpectedMissionLiveFiles) fail with ENOTDIR.
func breakMissionTree(t *testing.T, repo string) {
	t.Helper()
	missions := filepath.Join(repo, ".punt-labs", "ethos", "missions")
	require.NoError(t, os.MkdirAll(filepath.Dir(missions), 0o700))
	require.NoError(t, os.WriteFile(missions, []byte("x"), 0o600))
}

// writeOrphanSessionCorrupt plants an orphan .corrupt (no chunk, no marker) in
// a session's sealed dir, so ScanSealedDir — and thus SessionUnsealedCount —
// fails loud.
func writeOrphanSessionCorrupt(t *testing.T, repo, sessionID string) {
	t.Helper()
	dir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-07-21-"+sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	name := audit.SessionChunkFile(100, 200) + ".corrupt"
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
}

func TestPurgeTombstoned_SessionProbeErrorRefuses(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	require.NoError(t, s.Create("sess-sp", root, primary, repo, ""))
	// Only the SessionUnsealedCount probe fails (orphan .corrupt in the session
	// sealed dir); the mission probes succeed. Guards the SessionUnsealedCount
	// swallow specifically — a revert of it would let this session purge.
	writeOrphanSessionCorrupt(t, repo, "sess-sp")

	purged, refused, err := s.PurgeTombstoned(false)
	require.NoError(t, err)
	assert.Empty(t, purged)
	assert.Contains(t, refused, "sess-sp")
}

func TestPurgeTombstoned_MissionProbeErrorRefuses(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.Create("sess-mp", root, primary, repo, ""))
	// A valid mission chunk carrying the session (so it is enumerated with a
	// present live log) plus an orphan .corrupt for a different range, so
	// MissionUnsealedCount's watermark scan fails. Isolates that swallow.
	mid := "m-2026-07-21-050"
	mdir := filepath.Join(repo, ".punt-labs", "ethos", "missions", mid)
	require.NoError(t, os.MkdirAll(mdir, 0o700))
	chunk := `{"ts":"` + audit.FormatLineTS(100) + `","event":"create","actor":"a"}` + "\n" +
		`{"ts":"` + audit.FormatLineTS(200) + `","event":"close","actor":"a"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(mdir, audit.MissionChunkFile("sess-mp", 100, 200)), []byte(chunk), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mdir, audit.MissionChunkFile("sess-mp", 300, 400)+".corrupt"), []byte("x"), 0o600))
	live := audit.LiveMissionLogPath(repo, mid, "sess-mp")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte(`{"ts":"`+audit.FormatLineTS(250)+`","event":"update","actor":"a"}`+"\n"), 0o600))

	purged, refused, err := s.PurgeTombstoned(false)
	require.NoError(t, err)
	assert.Empty(t, purged)
	assert.Contains(t, refused, "sess-mp")
}

func TestPurgeTombstoned_TombstoneWriteFailureRefuses(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.Create("sess-tb", root, primary, repo, ""))
	// No live file → liveGone → a tombstone is attempted. Block its write by
	// planting a directory at the tombstone path; the roster must survive.
	require.NoError(t, os.MkdirAll(filepath.Join(s.sessionsDir(), "sess-tb.purged"), 0o700))

	purged, refused, err := s.PurgeTombstoned(false)
	require.NoError(t, err)
	assert.Empty(t, purged)
	assert.Contains(t, refused, "sess-tb")
	ids, err := s.List()
	require.NoError(t, err)
	assert.Contains(t, ids, "sess-tb", "a failed tombstone write must keep the roster")
}

func TestPurgeTombstoned_TombstoneWriteFailureRefusesUnderForce(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.Create("sess-tbf", root, primary, repo, ""))
	require.NoError(t, os.MkdirAll(filepath.Join(s.sessionsDir(), "sess-tbf.purged"), 0o700))

	// --force overrides an unprovable state, never a failed loss record.
	purged, refused, err := s.PurgeTombstoned(true)
	require.NoError(t, err)
	assert.Empty(t, purged)
	assert.Contains(t, refused, "sess-tbf")
	ids, err := s.List()
	require.NoError(t, err)
	assert.Contains(t, ids, "sess-tbf")
}

func TestPurgeTombstoned_ProbeErrorRefuses(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	require.NoError(t, s.Create("sess-probe", root, primary, repo, ""))
	breakMissionTree(t, repo)

	// A probe error must not read as "nothing unsealed" — fail safe: refuse.
	purged, refused, err := s.PurgeTombstoned(false)
	require.NoError(t, err)
	assert.Empty(t, purged)
	assert.Contains(t, refused, "sess-probe")

	ids, err := s.List()
	require.NoError(t, err)
	assert.Contains(t, ids, "sess-probe", "a refused probe must not delete the session")
}

func TestPurgeTombstoned_ProbeErrorForceFlagsTombstone(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.Create("sess-probe-force", root, primary, repo, ""))
	breakMissionTree(t, repo)

	purged, refused, err := s.PurgeTombstoned(true)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-probe-force")
	assert.Empty(t, refused)

	tb, err := audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-probe-force.purged"))
	require.NoError(t, err)
	assert.True(t, tb.UnsealedLines, "an unprovable state must flag the tombstone under --force")
}

func TestPurgeTombstoned_CorruptRosterRefusesWithoutForce(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.Create("sess-corrupt", root, primary, repo, ""))
	// Corrupt the roster so Load fails — a crash artifact.
	require.NoError(t, os.WriteFile(s.rosterPath("sess-corrupt"), []byte("[unclosed"), 0o600))

	purged, refused, err := s.PurgeTombstoned(false)
	require.NoError(t, err)
	assert.Empty(t, purged)
	assert.Contains(t, refused, "sess-corrupt", "an unreadable roster must refuse without --force")

	ids, err := s.List()
	require.NoError(t, err)
	assert.Contains(t, ids, "sess-corrupt")
}

func TestPurgeTombstoned_CorruptRosterForcePurges(t *testing.T) {
	s := testStore(t)
	repo := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"}
	require.NoError(t, s.Create("sess-corrupt-force", root, primary, repo, ""))
	require.NoError(t, os.WriteFile(s.rosterPath("sess-corrupt-force"), []byte("[unclosed"), 0o600))

	purged, refused, err := s.PurgeTombstoned(true)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-corrupt-force")
	assert.Empty(t, refused)
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
