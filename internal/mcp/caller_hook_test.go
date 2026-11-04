package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookHandler is a handler whose OpenCode _meta pilot is off, as under Claude
// Code, with three sessions:
//
//	sess-lead   leader keyed on its CLAUDE_PID (4242, alice), subagent agent-sub1 (carol),
//	            persona-less subagent agent-gp (agent_type general-purpose)
//	sess-outer  leader keyed on 999 (bob): the session a nested claude -p inherits
//	sess-inner  leader keyed on 4242 (alice): the nested session itself
func hookHandler(t *testing.T) *Handler {
	t.Helper()
	h, _ := metadataHandler(t)
	WithOpenCodeSessionMetadata(false)(h)
	rosters := []struct {
		sid string
		ps  []session.Participant
	}{
		{"sess-lead", []session.Participant{
			{AgentID: "4242", Persona: "alice"},
			{AgentID: "agent-sub1", Persona: "carol", Parent: "4242"},
			{AgentID: "agent-gp", AgentType: "general-purpose", Parent: "4242"},
		}},
		{"sess-outer", []session.Participant{{AgentID: "999", Persona: "bob"}}},
		{"sess-inner", []session.Participant{{AgentID: "4242", Persona: "carol"}}},
	}
	for _, r := range rosters {
		require.NoError(t, h.sessionStore.Create(r.sid, session.Participant{AgentID: "human", Persona: "bob"}, r.ps[0], "", ""))
		for _, p := range r.ps[1:] {
			require.NoError(t, h.sessionStore.Join(r.sid, p))
		}
	}
	return h
}

func declared(sid, agent string) map[string]any {
	return map[string]any{"session_id": sid, "agent_id": agent}
}

func whoami(t *testing.T, h *Handler, args map[string]any) (*mcplib.CallToolResult, string) {
	t.Helper()
	args["method"] = "whoami"
	result, err := h.handleIdentity(context.Background(), callTool(args))
	require.NoError(t, err)
	return result, resultText(t, result)
}

func handleOf(t *testing.T, text string) string {
	t.Helper()
	var id identity.Identity
	require.NoError(t, json.Unmarshal([]byte(text), &id), text)
	return id.Handle
}

// The harness gives the MCP server no CLAUDE_PID, so a declared caller must be
// answered from the roster alone, whatever the process environment holds.
func TestDeclaredCaller_Whoami(t *testing.T) {
	h := hookHandler(t)
	t.Setenv("CLAUDE_PID", "999")
	t.Setenv("ETHOS_SESSION", "sess-outer")
	t.Setenv("ETHOS_AGENT_ID", "999")
	t.Setenv("USER", "bob")

	for _, tc := range []struct{ name, sid, agent, want string }{
		{"leader keyed on its CLAUDE_PID", "sess-lead", "4242", "alice"},
		{"subagent keyed on its agent_id, not its leader", "sess-lead", "agent-sub1", "carol"},
		{"nested session wins over the inherited outer one", "sess-inner", "4242", "carol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, text := whoami(t, h, map[string]any{callerArg: declared(tc.sid, tc.agent)})
			require.False(t, result.IsError, text)
			assert.Equal(t, tc.want, handleOf(t, text))
		})
	}
}

// A declared caller that is not an exact participant is an error; no other
// source answers for it.
func TestDeclaredCaller_NoFallback(t *testing.T) {
	h := hookHandler(t)
	t.Setenv("ETHOS_SESSION", "sess-lead")
	t.Setenv("ETHOS_AGENT_ID", "4242")
	for _, tc := range []struct{ name, sid, agent, want string }{
		{"unknown participant", "sess-lead", "7777", "not found in roster"},
		{"unknown session", "sess-none", "4242", "loading caller roster"},
		{"participant of another session", "sess-outer", "4242", "not found in roster"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, text := whoami(t, h, map[string]any{callerArg: declared(tc.sid, tc.agent)})
			assert.True(t, result.IsError, text)
			assert.Contains(t, text, tc.want)
		})
	}
}

func TestDeclaredCaller_Malformed(t *testing.T) {
	for name, v := range map[string]any{
		"null":            nil,
		"string":          "sess-lead",
		"list":            []any{"sess-lead", "4242"},
		"number":          4242,
		"empty":           map[string]any{},
		"no agent":        map[string]any{"session_id": "sess-lead"},
		"no session":      map[string]any{"agent_id": "4242"},
		"unsafe session":  declared("../sess-lead", "4242"),
		"unsafe agent":    declared("sess-lead", "a/b"),
		"blank agent":     declared("sess-lead", ""),
		"numeric agent":   map[string]any{"session_id": "sess-lead", "agent_id": 4242},
		"unknown key":     map[string]any{"session_id": "sess-lead", "agent_id": "4242", "persona": "bob"},
		"trailing space":  declared("sess-lead ", "4242"),
		"newline in id":   declared("sess-lead\n", "4242"),
		"nul in id":       declared("sess-lead", "42\x0042"),
		"dot dot session": declared("..", "4242"),
	} {
		t.Run(name, func(t *testing.T) {
			h := hookHandler(t)
			t.Setenv("ETHOS_SESSION", "sess-lead")
			t.Setenv("ETHOS_AGENT_ID", "4242")
			for _, method := range []string{"whoami", "roster", "iam", "doctor"} {
				var result *mcplib.CallToolResult
				var err error
				args := map[string]any{"method": method, "persona": "carol", callerArg: v}
				switch method {
				case "whoami":
					result, err = h.handleIdentity(context.Background(), callTool(args))
				case "doctor":
					result, err = h.handleDoctor(context.Background(), callTool(args))
				default:
					result, err = h.handleSession(context.Background(), callTool(args))
				}
				require.NoError(t, err)
				text := resultText(t, result)
				if method == "doctor" {
					assert.Contains(t, text, "FAIL", "doctor reports the bad caller on its Human identity row")
					assert.Contains(t, text, callerArg)
					continue
				}
				assert.True(t, result.IsError, "%s: %s", method, text)
				assert.Contains(t, text, callerArg, method)
			}
			roster, err := h.sessionStore.Load("sess-lead")
			require.NoError(t, err)
			assert.Equal(t, "alice", roster.FindParticipant("4242").Persona, "a rejected caller must not mutate")
		})
	}
}

// Without the hook (or a model-supplied value surviving), behaviour is the
// unchanged env/PID chain: no declared caller means no _caller handling.
func TestDeclaredCaller_AbsentIsUnchanged(t *testing.T) {
	h := hookHandler(t)
	t.Setenv("ETHOS_SESSION", "sess-lead")
	t.Setenv("ETHOS_AGENT_ID", "agent-sub1")
	result, text := whoami(t, h, map[string]any{})
	require.False(t, result.IsError, text)
	assert.Equal(t, "carol", handleOf(t, text), "resolved from ETHOS_SESSION and ETHOS_AGENT_ID as before")
}

func TestDeclaredCaller_RosterDefaultsToCallerSession(t *testing.T) {
	h := hookHandler(t)
	t.Setenv("ETHOS_SESSION", "sess-outer")
	result, err := h.handleSession(context.Background(), callTool(map[string]any{
		"method": "roster", callerArg: declared("sess-inner", "4242"),
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(t, result))
	var roster session.Roster
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &roster))
	assert.Equal(t, "sess-inner", roster.Session, "the nested session, not the outer one the env names")
}

// An explicit session_id is an administrative target and keeps that meaning:
// the declared caller does not override it, and need not belong to it.
func TestDeclaredCaller_ExplicitSessionIDIsAdministrativeTarget(t *testing.T) {
	h := hookHandler(t)
	c := declared("sess-lead", "4242")
	result, err := h.handleSession(context.Background(), callTool(map[string]any{
		"method": "roster", "session_id": "sess-outer", callerArg: c,
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(t, result))
	var roster session.Roster
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &roster))
	assert.Equal(t, "sess-outer", roster.Session)

	for _, args := range []map[string]any{
		{"method": "join", "agent_id": "newcomer", "persona": "carol"},
		{"method": "leave", "agent_id": "newcomer"},
	} {
		args["session_id"], args[callerArg] = "sess-outer", c
		result, err = h.handleSession(context.Background(), callTool(args))
		require.NoError(t, err)
		assert.False(t, result.IsError, resultText(t, result))
	}
}

func TestDeclaredCaller_Iam(t *testing.T) {
	h := hookHandler(t)
	c := declared("sess-lead", "agent-sub1")
	result, err := h.handleSession(context.Background(), callTool(map[string]any{
		"method": "iam", "persona": "bob", callerArg: c,
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(t, result))
	roster, err := h.sessionStore.Load("sess-lead")
	require.NoError(t, err)
	assert.Equal(t, "bob", roster.FindParticipant("agent-sub1").Persona, "the subagent's own participant changes")
	assert.Equal(t, "alice", roster.FindParticipant("4242").Persona, "its leader's does not")

	for name, args := range map[string]map[string]any{
		"other session": {"session_id": "sess-outer"},
		"other agent":   {"agent_id": "4242"},
		"bad persona":   {"persona": "missing"},
	} {
		t.Run(name, func(t *testing.T) {
			args["method"], args[callerArg] = "iam", c
			if _, ok := args["persona"]; !ok {
				args["persona"] = "carol"
			}
			result, err := h.handleSession(context.Background(), callTool(args))
			require.NoError(t, err)
			assert.True(t, result.IsError, resultText(t, result))
			roster, err := h.sessionStore.Load("sess-lead")
			require.NoError(t, err)
			assert.Equal(t, "bob", roster.FindParticipant("agent-sub1").Persona)
		})
	}
}

func TestDeclaredCaller_BothSourcesRefused(t *testing.T) {
	h, _ := metadataHandler(t)
	req := metadataCall(t, "ses_A", map[string]any{"method": "whoami", callerArg: declared("ses_A", "ses_A")})
	result, err := h.handleIdentity(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "both declare a caller")
}

func TestDeclaredCaller_Doctor(t *testing.T) {
	h := hookHandler(t)
	result, err := h.handleDoctor(context.Background(), callTool(map[string]any{
		callerArg: declared("sess-lead", "agent-sub1"),
	}))
	require.NoError(t, err)
	text := resultText(t, result)
	assert.Contains(t, text, "Human identity")
	assert.Contains(t, text, "Repo carol (carol)")

	result, err = h.handleDoctor(context.Background(), callTool(map[string]any{
		callerArg: declared("sess-lead", "7777"),
	}))
	require.NoError(t, err)
	assert.Contains(t, resultText(t, result), "not found in roster")
}

// Mission binding acts on the caller's own session.
func TestDeclaredCaller_MissionSession(t *testing.T) {
	h := hookHandler(t)
	t.Setenv("ETHOS_SESSION", "sess-outer")
	sid, err := h.callerSessionID(callTool(map[string]any{callerArg: declared("sess-inner", "4242")}))
	require.NoError(t, err)
	assert.Equal(t, "sess-inner", sid)

	_, err = h.callerSessionID(callTool(map[string]any{callerArg: declared("sess-inner", "7777")}))
	assert.ErrorContains(t, err, "not found in roster", "no fallback to the env session")

	sid, err = h.callerSessionID(callTool(map[string]any{}))
	require.NoError(t, err)
	assert.Equal(t, "sess-outer", sid, "without _caller the chain is unchanged")
}

// The reserved argument is declared in every caller-resolving tool's schema so
// the MCP client passes it through, and described as hook-set.
func TestDeclaredCaller_SchemaEntries(t *testing.T) {
	h := hookHandler(t)
	for _, tool := range []mcplib.Tool{h.identityTool(), h.sessionTool(), h.doctorTool(), h.missionTool()} {
		prop, ok := tool.InputSchema.Properties[callerArg].(map[string]any)
		require.True(t, ok, "%s declares %s", tool.Name, callerArg)
		assert.Equal(t, "object", prop["type"], tool.Name)
		assert.Equal(t, "Set by the ethos hook; do not supply.", prop["description"], tool.Name)
	}
}

// A participant with no persona (a built-in subagent such as general-purpose)
// has no ethos identity; DES-007 has query tools name its agent_type instead.
// It is an answer, not an error, and no other source answers for it.
func TestDeclaredCaller_PersonaLessWhoami(t *testing.T) {
	h := hookHandler(t)
	t.Setenv("CLAUDE_PID", "4242")
	t.Setenv("ETHOS_SESSION", "sess-lead")
	t.Setenv("ETHOS_AGENT_ID", "4242")
	t.Setenv("USER", "bob")

	result, text := whoami(t, h, map[string]any{callerArg: declared("sess-lead", "agent-gp")})
	require.False(t, result.IsError, text)
	assert.Contains(t, text, "no ethos identity")
	assert.Contains(t, text, "general-purpose")
	for _, other := range []string{"alice", "bob", "carol"} {
		assert.NotContains(t, text, other, "must not fall back to another identity")
	}

	t.Run("without agent_type it names the agent_id", func(t *testing.T) {
		require.NoError(t, h.sessionStore.Join("sess-lead", session.Participant{AgentID: "agent-bare", Parent: "4242"}))
		result, text := whoami(t, h, map[string]any{callerArg: declared("sess-lead", "agent-bare")})
		require.False(t, result.IsError, text)
		assert.Contains(t, text, "agent-bare")
	})
}

func TestDeclaredCaller_PersonaLessIam(t *testing.T) {
	h := hookHandler(t)
	c := declared("sess-lead", "agent-gp")

	result, err := h.handleSession(context.Background(), callTool(map[string]any{
		"method": "iam", "persona": "missing", callerArg: c,
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError, "an unknown persona is still refused")

	result, err = h.handleSession(context.Background(), callTool(map[string]any{
		"method": "iam", "persona": "bob", callerArg: c,
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(t, result))
	roster, err := h.sessionStore.Load("sess-lead")
	require.NoError(t, err)
	assert.Equal(t, "bob", roster.FindParticipant("agent-gp").Persona)
	assert.Equal(t, "general-purpose", roster.FindParticipant("agent-gp").AgentType, "agent_type is kept")
	assert.Equal(t, "alice", roster.FindParticipant("4242").Persona, "its leader's persona does not change")

	_, text := whoami(t, h, map[string]any{callerArg: c})
	assert.Equal(t, "bob", handleOf(t, text))
}

func TestDeclaredCaller_PersonaLessRosterMissionDoctor(t *testing.T) {
	h := hookHandler(t)
	c := declared("sess-lead", "agent-gp")

	result, err := h.handleSession(context.Background(), callTool(map[string]any{"method": "roster", callerArg: c}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(t, result))
	var roster session.Roster
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &roster))
	assert.Equal(t, "sess-lead", roster.Session)

	sid, err := h.callerSessionID(callTool(map[string]any{callerArg: c}))
	require.NoError(t, err)
	assert.Equal(t, "sess-lead", sid)

	result, err = h.handleDoctor(context.Background(), callTool(map[string]any{callerArg: c}))
	require.NoError(t, err)
	text := resultText(t, result)
	assert.Contains(t, text, "Human identity")
	assert.Contains(t, text, "general-purpose")
	assert.NotContains(t, text, "invalid caller persona")
}
