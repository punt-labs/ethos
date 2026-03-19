//go:build !windows

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func TestStore_CreateAndLoad(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "jfreeman", Persona: "jfreeman"}
	primary := Participant{AgentID: "12345", Persona: "archie", Parent: "jfreeman"}
	require.NoError(t, s.Create("session-1", root, primary))

	roster, err := s.Load("session-1")
	require.NoError(t, err)
	assert.Equal(t, "session-1", roster.Session)
	assert.NotEmpty(t, roster.Started)
	assert.Len(t, roster.Participants, 2)
	assert.Equal(t, "jfreeman", roster.Participants[0].AgentID)
	assert.Equal(t, "12345", roster.Participants[1].AgentID)
	assert.Equal(t, "jfreeman", roster.Participants[1].Parent)
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
	require.NoError(t, s.Create("sess-join", root, primary))

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
	require.NoError(t, s.Create("sess-idem", root, primary))

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
	require.NoError(t, s.Create("sess-leave", root, primary))

	sub := Participant{AgentID: "sub-1", Persona: "reviewer", Parent: "99999"}
	require.NoError(t, s.Join("sess-leave", sub))
	require.NoError(t, s.Leave("sess-leave", "sub-1"))

	roster, err := s.Load("sess-leave")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 2)
}

func TestStore_LeaveNotFound(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-leave2", root, primary))

	err := s.Leave("sess-leave2", "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_Delete(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-del", root, primary))

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
	require.NoError(t, s.Create("sess-a", root, primary))
	require.NoError(t, s.Create("sess-b", root, primary))

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
	require.NoError(t, s.Create("sess-stale", root, primary))

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
	require.NoError(t, s.Create("sess-live", root, primary))

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

func TestStore_ReadCurrentSessionNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.ReadCurrentSession("nonexistent")
	require.Error(t, err)
}

func TestStore_FilePermissions(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-perms", root, primary))

	info, err := os.Stat(filepath.Join(s.sessionsDir(), "sess-perms.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
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
	require.NoError(t, s.Create("sess-ext", root, primary))

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
