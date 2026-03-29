package hook

import (
	"bytes"
	"os"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"
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
