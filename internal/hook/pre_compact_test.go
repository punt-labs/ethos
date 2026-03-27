package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PreCompactResult mirrors the JSON structure emitted by HandlePreCompact.
type PreCompactResult struct {
	SystemMessage string `json:"systemMessage"`
}

// capturePreCompactOutput runs HandlePreCompact and captures stdout.
func capturePreCompactOutput(t *testing.T, input string, s *identity.Store, ss *session.Store) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Stdout = oldStdout
		w.Close()
		r.Close()
	})
	os.Stdout = w

	in := bytes.NewReader([]byte(input))
	require.NoError(t, HandlePreCompact(in, s, ss))

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

func TestHandlePreCompact_ValidSession_CondensedPersona(t *testing.T) {
	id := &identity.Identity{
		Name:         "Claude Agento",
		Handle:       "claude",
		Kind:         "agent",
		Personality:  "principal-engineer",
		WritingStyle: "concise-quantified",
		Talents:      []string{"formal-methods", "product-strategy"},
	}
	personality := "# Principal Engineer\n\nStrategic and thorough.\n\n- Think before acting\n- Data over adjectives\n- Simplicity first"
	writingStyle := "# Concise Quantified\n\nShort and data-driven.\n\n- Under 30 words\n- Lead with numbers\n- No weasel words"

	s, ss := setupIdentityWithAttributes(t, id, personality, writingStyle)

	// Create a session with a root human and a primary agent.
	root := session.Participant{AgentID: "jfreeman", Persona: "jim"}
	primary := session.Participant{AgentID: "12345", Persona: "claude", Parent: "jfreeman"}
	require.NoError(t, ss.Create("test-session", root, primary))

	payload, err := json.Marshal(map[string]string{"session_id": "test-session"})
	require.NoError(t, err)

	out := capturePreCompactOutput(t, string(payload), s, ss)

	var result PreCompactResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.SystemMessage
	assert.Contains(t, ctx, "Active persona: Claude Agento (claude)")
	assert.Contains(t, ctx, "Personality: principal-engineer")
	assert.Contains(t, ctx, "Think before acting")
	assert.Contains(t, ctx, "Writing: concise-quantified")
	assert.Contains(t, ctx, "Under 30 words")
	assert.Contains(t, ctx, "Talents: formal-methods, product-strategy")
}

func TestHandlePreCompact_NoSession_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// No session_id in payload.
	out := capturePreCompactOutput(t, `{}`, s, ss)
	assert.Equal(t, "", out)
}

func TestHandlePreCompact_SessionNotFound_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// session_id points to a non-existent session.
	out := capturePreCompactOutput(t, `{"session_id": "missing"}`, s, ss)
	assert.Equal(t, "", out)
}

func TestHandlePreCompact_NoPersonaOnAgent_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// Create a session where the agent has no persona.
	root := session.Participant{AgentID: "jfreeman", Persona: "jim"}
	primary := session.Participant{AgentID: "99999", Persona: "", Parent: "jfreeman"}
	require.NoError(t, ss.Create("test-session", root, primary))

	out := capturePreCompactOutput(t, `{"session_id": "test-session"}`, s, ss)
	assert.Equal(t, "", out)
}

func TestHandlePreCompact_PersonaIdentityNotFound_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// Create a session where the agent has a persona but the identity file is missing.
	root := session.Participant{AgentID: "jfreeman", Persona: "jim"}
	primary := session.Participant{AgentID: "99999", Persona: "ghost", Parent: "jfreeman"}
	require.NoError(t, ss.Create("test-session", root, primary))

	out := capturePreCompactOutput(t, `{"session_id": "test-session"}`, s, ss)
	assert.Equal(t, "", out)
}
