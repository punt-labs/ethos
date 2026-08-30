//go:build !windows

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// TestStore_IDPathSanitization pins the ID→path defense at the exact line
// it lives: rosterPath and lockPath run the session ID through
// filepath.Base, so a traversal-shaped ID (from an attacker-controlled
// ETHOS_SESSION) can never derive a path outside the sessions dir. The
// store sanitizes rather than rejects — this table asserts sanitization.
// If filepath.Base is ever dropped, this fails loudly here rather than
// only in the end-to-end subprocess tests.
func TestStore_IDPathSanitization(t *testing.T) {
	s := testStore(t)
	sessionsDir := s.sessionsDir()

	cases := []struct {
		name string
		id   string
	}{
		{"clean", "abc123"},
		{"dotdot", "../escape"},
		{"dotdot-deep", "../../../../etc/passwd"},
		{"absolute", "/etc/passwd"},
		{"embedded-slash", "a/b/c"},
		{"trailing-slash", "evil/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range []string{s.rosterPath(tc.id), s.lockPath(tc.id)} {
				assert.Equal(t, sessionsDir, filepath.Dir(p),
					"derived path %q must sit directly in the sessions dir", p)
				rel, err := filepath.Rel(sessionsDir, p)
				require.NoError(t, err)
				assert.False(t, strings.HasPrefix(rel, ".."),
					"derived path %q must not escape the sessions dir (rel %q)", p, rel)
			}
		})
	}
}

func TestStore_CreateAndLoad(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "mal", Persona: "mal"}
	primary := Participant{AgentID: "12345", Persona: "archie", Parent: "mal"}
	require.NoError(t, s.Create("session-1", root, primary, "", ""))

	roster, err := s.Load("session-1")
	require.NoError(t, err)
	assert.Equal(t, "session-1", roster.Session)
	assert.NotEmpty(t, roster.Started)
	assert.Len(t, roster.Participants, 2)
	assert.Equal(t, "mal", roster.Participants[0].AgentID)
	assert.Equal(t, "12345", roster.Participants[1].AgentID)
	assert.Equal(t, "mal", roster.Participants[1].Parent)
}

func TestStore_LoadNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.Load("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_Join(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-join", root, primary, "", ""))

	sub := Participant{
		AgentID:   "sub-1",
		Persona:   "code-reviewer",
		AgentType: "code-reviewer",
		Parent:    "99999",
	}
	require.NoError(t, s.Join("sess-join", sub))

	roster, err := s.Load("sess-join")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 3)
	assert.Equal(t, "sub-1", roster.Participants[2].AgentID)
	assert.Equal(t, "code-reviewer", roster.Participants[2].Persona)
}

func TestStore_JoinIdempotent(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-idem", root, primary, "", ""))

	sub := Participant{AgentID: "sub-1", Persona: "reviewer", Parent: "99999"}
	require.NoError(t, s.Join("sess-idem", sub))

	// Re-join with updated persona.
	sub2 := Participant{AgentID: "sub-1", Persona: "updated-reviewer", Parent: "99999"}
	require.NoError(t, s.Join("sess-idem", sub2))

	roster, err := s.Load("sess-idem")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 3)
	assert.Equal(t, "updated-reviewer", roster.FindParticipant("sub-1").Persona)
}

func TestStore_Leave(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-leave", root, primary, "", ""))

	sub := Participant{AgentID: "sub-1", Persona: "reviewer", Parent: "99999"}
	require.NoError(t, s.Join("sess-leave", sub))
	require.NoError(t, s.Leave("sess-leave", "sub-1"))

	roster, err := s.Load("sess-leave")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 2)
}

func TestStore_LeaveIdempotent(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-leave2", root, primary, "", ""))

	// Leaving a session you were never in is a no-op, not an error.
	err := s.Leave("sess-leave2", "nonexistent")
	require.NoError(t, err)
}

func TestStore_Delete(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-del", root, primary, "", ""))

	require.NoError(t, s.Delete("sess-del"))

	_, err := s.Load("sess-del")
	require.Error(t, err)
}

func TestStore_DeleteNonexistent(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.Delete("nonexistent"))
}

func TestStore_List(t *testing.T) {
	s := testStore(t)

	// Empty list.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)

	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-a", root, primary, "", ""))
	require.NoError(t, s.Create("sess-b", root, primary, "", ""))

	ids, err = s.List()
	require.NoError(t, err)
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, "sess-a")
	assert.Contains(t, ids, "sess-b")
}

func TestStore_ListNoDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nonexistent"))
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestStore_Purge(t *testing.T) {
	s := testStore(t)

	// Create a roster with a dead PID as primary agent.
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "9999999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-stale", root, primary, "", ""))

	purged, err := s.Purge()
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-stale")

	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestStore_PurgeKeepsLive(t *testing.T) {
	s := testStore(t)

	// Create a roster with our own PID as primary (definitely alive).
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{
		AgentID: fmt.Sprintf("%d", os.Getpid()),
		Persona: "agent",
		Parent:  "user1",
	}
	require.NoError(t, s.Create("sess-live", root, primary, "", ""))

	purged, err := s.Purge()
	require.NoError(t, err)
	assert.Empty(t, purged)

	ids, err := s.List()
	require.NoError(t, err)
	assert.Contains(t, ids, "sess-live")
}

func TestStore_CurrentSession(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.WriteCurrentSession("12345", "sess-abc"))

	id, err := s.ReadCurrentSession("12345")
	require.NoError(t, err)
	assert.Equal(t, "sess-abc", id)

	require.NoError(t, s.DeleteCurrentSession("12345"))

	_, err = s.ReadCurrentSession("12345")
	require.Error(t, err)
}

func TestStore_PurgeCurrentFiles(t *testing.T) {
	s := testStore(t)

	// Write PID files: one for our own PID (alive), one for a dead PID.
	alivePID := fmt.Sprintf("%d", os.Getpid())
	deadPID := "99999999"

	require.NoError(t, s.WriteCurrentSession(alivePID, "sess-alive"))
	require.NoError(t, s.WriteCurrentSession(deadPID, "sess-dead"))

	purged, err := s.PurgeCurrent()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{deadPID}, purged)

	// Alive PID file still exists.
	sid, err := s.ReadCurrentSession(alivePID)
	require.NoError(t, err)
	assert.Equal(t, "sess-alive", sid)

	// Dead PID file is gone.
	_, err = s.ReadCurrentSession(deadPID)
	require.Error(t, err)
}

func TestStore_PurgeCurrentNoDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nonexistent"))
	purged, err := s.PurgeCurrent()
	require.NoError(t, err)
	assert.Empty(t, purged)
}

func TestStore_PurgeCleansBothRostersAndPIDFiles(t *testing.T) {
	s := testStore(t)

	deadPID := "99999999"

	// Create a roster with a dead PID as primary.
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: deadPID, Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-both", root, primary, "", ""))

	// Write a PID file for the same dead PID.
	require.NoError(t, s.WriteCurrentSession(deadPID, "sess-both"))

	purged, err := s.Purge()
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-both")

	// Roster is gone.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)

	// PurgeCurrent is now called separately (CLI orchestrates both).
	pidPurged, pidErr := s.PurgeCurrent()
	require.NoError(t, pidErr)
	assert.ElementsMatch(t, []string{deadPID}, pidPurged)

	// PID file is also gone.
	_, err = s.ReadCurrentSession(deadPID)
	require.Error(t, err)
}

func TestStore_ReadCurrentSessionNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.ReadCurrentSession("nonexistent")
	require.Error(t, err)
}

func TestStore_FilePermissions(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-perms", root, primary, "", ""))

	info, err := os.Stat(filepath.Join(s.sessionsDir(), "sess-perms.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestIsStale pins that staleness keys on AGE, not participant count: a
// solo session (one participant, from Store.Create's root==primary collapse)
// ages out by the TTL rather than being immediately reclaimed. Only a
// genuinely empty roster is stale on sight.
func TestIsStale(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)

	t.Run("fresh solo session is not stale", func(t *testing.T) {
		r := &Roster{Started: now, Participants: []Participant{{AgentID: "jim", Persona: "jim"}}}
		assert.False(t, isStale(r))
	})
	t.Run("solo session past TTL is stale", func(t *testing.T) {
		r := &Roster{Started: old, Participants: []Participant{{AgentID: "jim", Persona: "jim"}}}
		assert.True(t, isStale(r))
	})
	t.Run("empty roster is stale", func(t *testing.T) {
		r := &Roster{Started: now, Participants: nil}
		assert.True(t, isStale(r))
	})
	t.Run("fresh persona primary is not stale", func(t *testing.T) {
		r := &Roster{Started: now, Participants: []Participant{{AgentID: "jim"}, {AgentID: "bwk", Parent: "jim"}}}
		assert.False(t, isStale(r))
	})
	t.Run("persona primary past TTL is stale", func(t *testing.T) {
		r := &Roster{Started: old, Participants: []Participant{{AgentID: "jim"}, {AgentID: "bwk", Parent: "jim"}}}
		assert.True(t, isStale(r))
	})
	t.Run("live numeric primary is not stale even past TTL", func(t *testing.T) {
		pid := strconv.Itoa(os.Getpid())
		r := &Roster{Started: old, Participants: []Participant{{AgentID: "jim"}, {AgentID: pid, Parent: "jim"}}}
		assert.False(t, isStale(r), "a live PID wins over the age fallback")
	})
}

func TestRoster_FindParticipant(t *testing.T) {
	r := &Roster{
		Participants: []Participant{
			{AgentID: "a1", Persona: "alice"},
			{AgentID: "b2", Persona: "bob"},
		},
	}
	assert.NotNil(t, r.FindParticipant("a1"))
	assert.Equal(t, "alice", r.FindParticipant("a1").Persona)
	assert.Nil(t, r.FindParticipant("nonexistent"))
}

func TestRoster_RemoveParticipant(t *testing.T) {
	r := &Roster{
		Participants: []Participant{
			{AgentID: "a1", Persona: "alice"},
			{AgentID: "b2", Persona: "bob"},
		},
	}
	assert.True(t, r.RemoveParticipant("a1"))
	assert.Len(t, r.Participants, 1)
	assert.False(t, r.RemoveParticipant("a1"))
}

func TestStore_JoinWithExt(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-ext", root, primary, "", ""))

	sub := Participant{
		AgentID: "sub-1",
		Persona: "reviewer",
		Parent:  "99999",
		Ext:     map[string]any{"biff": map[string]any{"tty": "s004"}},
	}
	require.NoError(t, s.Join("sess-ext", sub))

	roster, err := s.Load("sess-ext")
	require.NoError(t, err)
	p := roster.FindParticipant("sub-1")
	require.NotNil(t, p)
	assert.NotNil(t, p.Ext)
	biff, ok := p.Ext["biff"]
	require.True(t, ok)
	biffMap, ok := biff.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "s004", biffMap["tty"])
}

func TestStore_CreateWithRepoAndHost(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-repo", root, primary, "punt-labs/ethos", "m2-mb-air"))

	roster, err := s.Load("sess-repo")
	require.NoError(t, err)
	assert.Equal(t, "punt-labs/ethos", roster.Repo)
	assert.Equal(t, "m2-mb-air", roster.Host)
	assert.NotEmpty(t, roster.Participants[0].Joined)
	assert.NotEmpty(t, roster.Participants[1].Joined)
}

func TestStore_CreateSetsJoinedOnParticipants(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-joined", root, primary, "", ""))

	roster, err := s.Load("sess-joined")
	require.NoError(t, err)
	assert.NotEmpty(t, roster.Participants[0].Joined)
	assert.NotEmpty(t, roster.Participants[1].Joined)
}

func TestStore_JoinSetsJoined(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-join-ts", root, primary, "", ""))

	sub := Participant{AgentID: "sub-1", Persona: "reviewer", Parent: "99999"}
	require.NoError(t, s.Join("sess-join-ts", sub))

	roster, err := s.Load("sess-join-ts")
	require.NoError(t, err)
	p := roster.FindParticipant("sub-1")
	require.NotNil(t, p)
	assert.NotEmpty(t, p.Joined)
}

func TestStore_BackwardCompatibility_NoRepoHostJoined(t *testing.T) {
	s := testStore(t)

	// Write a roster in the old format (no repo, host, or joined fields).
	require.NoError(t, os.MkdirAll(s.sessionsDir(), 0o700))
	oldYAML := `session: sess-old
started: "2025-01-01T00:00:00Z"
participants:
  - agent_id: user1
    persona: user1
  - agent_id: "12345"
    persona: agent
    parent: user1
`
	require.NoError(t, os.WriteFile(
		filepath.Join(s.sessionsDir(), "sess-old.yaml"),
		[]byte(oldYAML), 0o600,
	))

	roster, err := s.Load("sess-old")
	require.NoError(t, err)
	assert.Equal(t, "sess-old", roster.Session)
	assert.Equal(t, "", roster.Repo)
	assert.Equal(t, "", roster.Host)
	assert.Len(t, roster.Participants, 2)
	assert.Equal(t, "", roster.Participants[0].Joined)
	assert.Equal(t, "", roster.Participants[1].Joined)
}

func TestStore_CreatePreservesExplicitJoined(t *testing.T) {
	s := testStore(t)

	// If Joined is already set, Create should not overwrite it.
	root := Participant{AgentID: "user1", Persona: "user1", Joined: "2025-01-01T00:00:00Z"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-preserve", root, primary, "", ""))

	roster, err := s.Load("sess-preserve")
	require.NoError(t, err)
	assert.Equal(t, "2025-01-01T00:00:00Z", roster.Participants[0].Joined)
	assert.NotEmpty(t, roster.Participants[1].Joined)
	assert.NotEqual(t, "2025-01-01T00:00:00Z", roster.Participants[1].Joined)
}

// TestStore_CreateInCheckoutRecordsCheckout pins the DES-058 binding the
// vacuum cross-check reads: the roster records the checkout whose live audit
// zone the session writes to, distinct from the git-remote identity. Create
// records none, which reads as "writer unknown".
func TestStore_CreateInCheckoutRecordsCheckout(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "99999", Parent: "user1"}

	require.NoError(t, s.CreateInCheckout("sess-co", root, primary,
		"punt-labs/ethos", "/checkouts/ethos", "host1"))
	roster, err := s.Load("sess-co")
	require.NoError(t, err)
	assert.Equal(t, "punt-labs/ethos", roster.Repo)
	assert.Equal(t, "/checkouts/ethos", roster.Checkout)

	require.NoError(t, s.Create("sess-noco", root, primary, "punt-labs/ethos", "host1"))
	roster, err = s.Load("sess-noco")
	require.NoError(t, err)
	assert.Equal(t, "", roster.Checkout)
}

// TestPurgeTombstoned_SealedMissionInNonWriterCheckoutDoesNotFlagTombstone is
// Fix 4 of ethos-q6e2. The mission-namespace guard used to read an absent live
// log as loss, so a purge run anywhere but the writing checkout minted a
// PERMANENT flagged tombstone — a loss record that was never earned and warns
// at every commit until acked.
//
// The suppression is narrower than "a chunk exists", and this test's setup is
// what makes it apply: the roster records NO checkout, so the purge falls back
// to whichever one it runs in, and that checkout holds no live mission log of
// this session's. Nothing ever said the session's files belong here, so their
// absence says nothing. A checkout the roster DID record still flags —
// TestPurgeTombstoned_DeletedLiveLogInWriterCheckoutFlagsTombstone covers it.
//
// The session's own live audit file is written and fully sealed here, so the
// session-namespace probe cannot supply the flag: only the mission branch is
// under test.
func TestPurgeTombstoned_SealedMissionInNonWriterCheckoutDoesNotFlagTombstone(t *testing.T) {
	s := testStore(t)
	repoRoot := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	// A roster predating the checkout field: the probe falls back to repoRoot.
	require.NoError(t, s.Create("sess-sealed", root, primary, testRepoID, ""))
	writeSealedLive(t, repoRoot, "sess-sealed")
	// A mission whose chunk carries this session, with no live log in this
	// checkout — the steady state of every checkout that did not write them.
	sealMissionChunkFor(t, repoRoot, "m-2026-07-21-009", "sess-sealed")

	purged, refused, err := s.PurgeTombstoned(repoRoot, testRepoID, false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-sealed")
	assert.Empty(t, refused)

	_, err = audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-sealed.purged"))
	assert.Error(t, err, "a session whose mission lines are all sealed must leave no loss tombstone")
}

// writeSealedLive writes a live audit line for sessionID under repoRoot and
// seals it into a tracked chunk, so the session-namespace probe reports the
// file present with nothing unsealed.
func writeSealedLive(t *testing.T, repoRoot, sessionID string) {
	t.Helper()
	live := audit.LiveAuditPath(repoRoot, sessionID)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	body := `{"ts":"` + audit.FormatLineTS(100) + `","session":"` + sessionID + `","tool":"Read"}` + "\n"
	require.NoError(t, os.WriteFile(live, []byte(body), 0o600))

	dir := filepath.Join(audit.SealedSessionsBase(repoRoot), "2026-07-21-"+sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte(body), 0o600))
}

// TestPurgeTombstoned_OutOfRepoUsesRecordedCheckout covers a purge run with no
// checkout in scope for a session whose roster records one. The recorded
// checkout is then the only checkout that can be named, and it holds both the
// live zone and its own tracked chunks. Reading the tracked side from an empty
// repoRoot instead would resolve ".punt-labs/..." against the working
// directory — nothing, or some unrelated repo's chunks — and flag a loss that
// did not happen.
func TestPurgeTombstoned_OutOfRepoUsesRecordedCheckout(t *testing.T) {
	s := testStore(t)
	checkout := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	// Repo "" is a checkout with no parseable origin — the only kind that
	// reaches the probes when the purge runs outside any repo.
	require.NoError(t, s.CreateInCheckout("sess-oor", root, primary, "", checkout, ""))
	writeSealedLive(t, checkout, "sess-oor")
	sealMissionChunkFor(t, checkout, "m-2026-07-21-009", "sess-oor")
	// The recorded checkout really holds the mission's live log, so there is
	// nothing missing there to report.
	writeLiveMissionLogFor(t, checkout, "m-2026-07-21-009", "sess-oor")

	purged, refused, err := s.PurgeTombstoned("", "", false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-oor")
	assert.Empty(t, refused)

	_, err = audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-oor.purged"))
	assert.Error(t, err, "state provable at the recorded checkout must not flag a loss")
}

// TestPurgeTombstoned_DeletedLiveLogInWriterCheckoutFlagsTombstone is the other
// side of the suppression, on the purge path. Here the checkout DID write
// mission live logs — a sibling mission's file stands — so this mission's
// missing file is a deletion, and the tail written after the chunk's watermark
// went with it. Purge must record that loss.
//
// Without the WriterZone distinction a sealed chunk suppressed unconditionally
// and the whole deletion class purged silently (rsc, PR #413 M1).
func TestPurgeTombstoned_DeletedLiveLogInWriterCheckoutFlagsTombstone(t *testing.T) {
	s := testStore(t)
	repoRoot := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	require.NoError(t, s.CreateInCheckout("sess-del", root, primary, testRepoID, repoRoot, ""))
	writeSealedLive(t, repoRoot, "sess-del")
	sealMissionChunkFor(t, repoRoot, "m-2026-07-21-009", "sess-del")
	// The live-missions zone exists here — this checkout wrote mission logs —
	// so m-2026-07-21-009's absent file is a deletion, not another checkout's
	// ordinary absence.
	writeLiveMissionLogFor(t, repoRoot, "m-2026-07-21-010", "sess-del")

	purged, refused, err := s.PurgeTombstoned(repoRoot, testRepoID, false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-del")
	assert.Empty(t, refused)

	tb, err := audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-del.purged"))
	require.NoError(t, err, "a deletion in the writer's own checkout must leave a tombstone")
	assert.True(t, tb.LiveFileGone, "the deleted mission live log must set the flag")
}

// writeLiveMissionLogFor writes one sealed-and-clean live mission log, enough
// to establish that the checkout has a live-missions zone.
func writeLiveMissionLogFor(t *testing.T, repoRoot, missionID, sessionID string) {
	t.Helper()
	path := audit.LiveMissionLogPath(repoRoot, missionID, sessionID)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	line := `{"ts":"` + audit.FormatLineTS(100) + `","event":"create"}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
	// Seal it, so this sibling contributes no unsealed lines of its own.
	dir := audit.SealedMissionDir(repoRoot, missionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, audit.MissionChunkFile(sessionID, 100, 100)), []byte(line), 0o600))
}

// TestPurgeTombstoned_RecordedWriterHoldsNoLiveLogFlagsTombstone pins the
// WIRING of the purge recorded-writer path, which no other test reaches.
//
// TestPurgeTombstoned_DeletedLiveLogInWriterCheckoutFlagsTombstone plants a
// sibling live log, so WriterZone carries its verdict and the recorded binding
// is never load-bearing. Here the recorded checkout exists and holds no live
// mission log at all — the whole live-missions zone removed — so only the
// binding separates it from a fallback probe. Downgrading RecordedWriter to
// AssumedWriter on this path restores the whole-zone-deleted hole, and this is
// the test that catches it.
func TestPurgeTombstoned_RecordedWriterHoldsNoLiveLogFlagsTombstone(t *testing.T) {
	s := testStore(t)
	repoRoot := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	require.NoError(t, s.CreateInCheckout("sess-nozone", root, primary, testRepoID, repoRoot, ""))
	// The session's own audit file is present and sealed, so the
	// session-namespace probe reports clean and only the mission branch can
	// supply the flag.
	writeSealedLive(t, repoRoot, "sess-nozone")
	// A sealed mission, and NO live-missions zone anywhere in this checkout.
	sealMissionChunkFor(t, repoRoot, "m-2026-07-21-009", "sess-nozone")

	purged, refused, err := s.PurgeTombstoned(repoRoot, testRepoID, false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-nozone")
	assert.Empty(t, refused)

	tb, err := audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-nozone.purged"))
	require.NoError(t, err,
		"a recorded writer that cannot produce the live log must leave a loss tombstone")
	assert.True(t, tb.LiveFileGone)
}
