package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSubagentStartOutput runs HandleSubagentStart and captures stdout.
func captureSubagentStartOutput(t *testing.T, input string, s identity.IdentityStore, ss *session.Store) string {
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
	require.NoError(t, HandleSubagentStart(in, s, ss))

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

func TestHandleSubagentStart_PersonaBlock(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// Create a personality and writing style for the subagent identity.
	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n\n- Simplicity first\n- Clarity over cleverness",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.\n\n- Under 25 words\n- Data over adjectives",
	}))
	ts := attribute.NewStore(dir, attribute.Talents)
	require.NoError(t, ts.Save(&attribute.Attribute{
		Slug:    "go-specialist",
		Content: "# Go Specialist",
	}))

	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
		Talents:      []string{"go-specialist"},
	}))

	// Create parent identity for the "reports to" line.
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Claude Agento",
		Handle: "claude",
		Kind:   "agent",
	}))

	// Create a session with a parent agent. Use FindClaudePID so
	// resolveParentLine can match the subagent's Parent field.
	claudePID := process.FindClaudePID()
	require.NoError(t, ss.Create("sub-test-1",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: claudePID, Persona: "claude"},
		"", "",
	))

	payload := `{
		"agent_id": "sub-1",
		"agent_type": "bwk",
		"session_id": "sub-test-1"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Brian K (bwk)")
	assert.Contains(t, ctx, "A methodical systems programmer.")
	assert.Contains(t, ctx, "## Personality")
	assert.Contains(t, ctx, "Simplicity first")
	assert.Contains(t, ctx, "## Writing Style")
	assert.Contains(t, ctx, "Under 25 words")
	assert.Contains(t, ctx, "## Talents")
	assert.Contains(t, ctx, "go-specialist")
	assert.Contains(t, ctx, "You report to Claude Agento (claude).")
	assert.Equal(t, "SubagentStart", result.HookSpecificOutput.HookEventName)
}

func TestHandleSubagentStart_WithExtensions(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n\n- Simplicity first",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.",
	}))

	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
	}))

	// Write extension with session_context.
	require.NoError(t, s.ExtSet("bwk", "quarry", "session_context", "memory instructions here"))

	claudePID := process.FindClaudePID()
	require.NoError(t, ss.Create("ext-test-1",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: claudePID, Persona: ""},
		"", "",
	))

	payload := `{
		"agent_id": "sub-ext-1",
		"agent_type": "bwk",
		"session_id": "ext-test-1"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Brian K (bwk)")
	assert.Contains(t, ctx, "A methodical systems programmer.")
	assert.Contains(t, ctx, "memory instructions here")
}

func TestHandleSubagentStart_NoExtensions(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n\n- Simplicity first",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.",
	}))

	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
	}))

	// No extensions written -- bwk.ext/ does not exist.

	claudePID := process.FindClaudePID()
	require.NoError(t, ss.Create("noext-test-1",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: claudePID, Persona: ""},
		"", "",
	))

	payload := `{
		"agent_id": "sub-noext-1",
		"agent_type": "bwk",
		"session_id": "noext-test-1"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Brian K (bwk)")
	assert.Contains(t, ctx, "A methodical systems programmer.")
	assert.NotContains(t, ctx, "memory instructions")
}

func TestHandleSubagentStart_ExtensionWithoutSessionContext(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n\n- Simplicity first",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.",
	}))

	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
	}))

	// Write extension with a key other than session_context.
	require.NoError(t, s.ExtSet("bwk", "quarry", "provider", "some-value"))

	claudePID := process.FindClaudePID()
	require.NoError(t, ss.Create("extnosc-test-1",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: claudePID, Persona: ""},
		"", "",
	))

	payload := `{
		"agent_id": "sub-extnosc-1",
		"agent_type": "bwk",
		"session_id": "extnosc-test-1"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Brian K (bwk)")
	assert.Contains(t, ctx, "A methodical systems programmer.")
	assert.NotContains(t, ctx, "some-value")
}

func TestHandleSubagentStart_NoMatchingPersona_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// Create a session but no identity for the agent_type.
	require.NoError(t, ss.Create("sub-test-2",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))

	payload := `{
		"agent_id": "sub-2",
		"agent_type": "code-reviewer",
		"session_id": "sub-test-2"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)
	assert.Equal(t, "", out)
}

func TestHandleSubagentStart_PersonaNoPersonality_GracefulFallback(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// Identity with no personality or writing style -- just name/handle/kind.
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Reviewer Bot",
		Handle: "reviewer",
		Kind:   "agent",
	}))

	require.NoError(t, ss.Create("sub-test-3",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))

	payload := `{
		"agent_id": "sub-3",
		"agent_type": "reviewer",
		"session_id": "sub-test-3"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	// No personality or writing style means BuildPersonaBlock returns "".
	// The handler should emit nothing in this case.
	assert.Equal(t, "", out)
}
