package hook

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStores(t *testing.T) (*identity.Store, *session.Store) {
	t.Helper()
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)
	return s, ss
}

func TestHandleSessionStart_CreatesRoster(t *testing.T) {
	s, ss := testStores(t)

	// Isolate git config so resolve chain uses USER env.
	tmp := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", tmp+"/empty.gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("USER", "alice")
	_ = os.WriteFile(tmp+"/empty.gitconfig", []byte(""), 0o644)

	require.NoError(t, s.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))

	input := bytes.NewReader([]byte(`{"session_id": "test-session-1"}`))
	err := HandleSessionStart(input, SessionStartDeps{Store: s, Sessions: ss})
	require.NoError(t, err)

	// Verify roster was created.
	roster, err := ss.Load("test-session-1")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 2) // human + claude
	assert.Equal(t, "alice", roster.Participants[0].Persona)
}

func TestHandleSessionStart_NoSessionID(t *testing.T) {
	s, ss := testStores(t)

	// No session_id → no roster, no error.
	input := bytes.NewReader([]byte(`{}`))
	err := HandleSessionStart(input, SessionStartDeps{Store: s, Sessions: ss})
	require.NoError(t, err)

	sessions, err := ss.List()
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestHandleSessionEnd_DeletesRoster(t *testing.T) {
	_, ss := testStores(t)

	// Create a roster first.
	require.NoError(t, ss.Create("test-end-1",
		session.Participant{AgentID: "user1", Persona: "alice"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))

	input := bytes.NewReader([]byte(`{"session_id": "test-end-1"}`))
	err := HandleSessionEnd(input, ss)
	require.NoError(t, err)

	// Verify roster is gone.
	_, loadErr := ss.Load("test-end-1")
	assert.Error(t, loadErr)
}

func TestHandleSessionEnd_NoSessionID(t *testing.T) {
	_, ss := testStores(t)

	input := bytes.NewReader([]byte(`{}`))
	err := HandleSessionEnd(input, ss)
	require.NoError(t, err)
}

// TestHandleSessionEnd_ClearsMissionBindings is review finding C6
// (m-2026-09-08-004 round 2): `claude --resume` reuses the session ID,
// so a claim or pending dispatch left over from before the session
// ended would otherwise survive into the resumed session and capture
// an entirely unrelated later spawn or commit. Session end is now
// treated the same as an explicit `ethos mission release`.
func TestHandleSessionEnd_ClearsMissionBindings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	// J5 (full-branch review, m-2026-09-08-004 round 3): HandleSessionEnd
	// now clears mission sidecars solely through ss.Delete (session.Store),
	// which resolves sidecars under ss's OWN root -- production always
	// constructs this store at $HOME/.punt-labs/ethos
	// (cmd/ethos/identity.go's sessionStore()), so the fixture must match
	// that, not testStores(t)'s independently-rooted temp dir (which
	// this test's mission.Write* calls below never wrote sidecars under,
	// a mismatch invisible before this fix only because the pre-J5
	// hook-local clearSessionMissionBindings resolved its own root via
	// os.UserHomeDir() independently of ss).
	ss := session.NewStore(globalRoot)

	sessionID := "sess-end-clears"
	require.NoError(t, ss.Create(sessionID,
		session.Participant{AgentID: "user1", Persona: "alice"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))
	require.NoError(t, mission.WriteActiveMission(globalRoot, sessionID, "m-2026-09-08-720"))
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, "m-2026-09-08-721", "bwk"))
	require.NoError(t, mission.WriteDelegationBinding(globalRoot, sessionID, mission.DelegationBinding{
		MissionID:    "m-2026-09-08-721",
		DelegationID: "d-2026-09-08-001",
	}))

	input := bytes.NewReader([]byte(`{"session_id": "` + sessionID + `"}`))
	require.NoError(t, HandleSessionEnd(input, ss))

	claimed, err := mission.ReadActiveMission(globalRoot, sessionID)
	require.NoError(t, err)
	assert.Empty(t, claimed, "a claim must not survive session end")

	pending, _, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	assert.Empty(t, pending, "a pending dispatch must not survive session end")

	// H2 (full-branch review, m-2026-09-08-004 round 3): the delegation-
	// binding sidecar is the third thing `ethos mission release` clears,
	// but session end was only clearing the other two. A survivor here
	// lets the commit-msg hook tag a later, unrelated session's commits
	// with a stale delegation (ethos-jawp's class).
	_, err = os.Stat(mission.DelegationBindingPath(globalRoot, sessionID))
	assert.True(t, os.IsNotExist(err), "the delegation-binding sidecar must not survive session end")
}

func TestHandleSubagentStart_JoinsRoster(t *testing.T) {
	s, ss := testStores(t)

	// Create a session first.
	require.NoError(t, ss.Create("test-sub-1",
		session.Participant{AgentID: "user1", Persona: "alice"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))

	input := bytes.NewReader([]byte(`{
		"agent_id": "sub-1",
		"agent_type": "code-reviewer",
		"session_id": "test-sub-1"
	}`))
	err := HandleSubagentStart(input, s, ss)
	require.NoError(t, err)

	roster, err := ss.Load("test-sub-1")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 3)
}

func TestHandleSubagentStart_MissingAgentID(t *testing.T) {
	s, ss := testStores(t)

	input := bytes.NewReader([]byte(`{"session_id": "test-sub-1"}`))
	err := HandleSubagentStart(input, s, ss)
	require.NoError(t, err) // No error, just no-op.
}

func TestHandleSubagentStop_LeavesRoster(t *testing.T) {
	_, ss := testStores(t)

	require.NoError(t, ss.Create("test-stop-1",
		session.Participant{AgentID: "user1", Persona: "alice"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))
	require.NoError(t, ss.Join("test-stop-1",
		session.Participant{AgentID: "sub-1", Persona: "reviewer"},
	))

	input := bytes.NewReader([]byte(`{
		"agent_id": "sub-1",
		"session_id": "test-stop-1"
	}`))
	err := HandleSubagentStop(input, ss)
	require.NoError(t, err)

	roster, err := ss.Load("test-stop-1")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 2) // sub-1 removed
}

func TestHandleSubagentStop_MissingFields(t *testing.T) {
	_, ss := testStores(t)

	input := bytes.NewReader([]byte(`{}`))
	err := HandleSubagentStop(input, ss)
	require.NoError(t, err) // No-op, no error.
}
