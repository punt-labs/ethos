package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Decode the wire shape rather than only constructing Meta in Go: this pins
// mcp-go's preservation of AdditionalFields from tools/call params._meta.
func metadataCall(t *testing.T, sid any, args map[string]any) mcplib.CallToolRequest {
	t.Helper()
	data, err := json.Marshal(map[string]any{"params": map[string]any{
		"_meta": map[string]any{opencodeSessionKey: sid}, "arguments": args,
	}})
	require.NoError(t, err)
	var req mcplib.CallToolRequest
	require.NoError(t, json.Unmarshal(data, &req))
	return req
}

func metadataHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	repo := identity.NewStore(filepath.Join(root, "repo"))
	global := identity.NewStore(filepath.Join(root, "global"))
	for _, handle := range []string{"alice", "bob", "carol"} {
		require.NoError(t, repo.Save(&identity.Identity{Name: "Repo " + handle, Handle: handle, Kind: "agent"}))
		require.NoError(t, global.Save(&identity.Identity{Name: "Global " + handle, Handle: handle, Kind: "human"}))
	}
	ss := session.NewStore(root)
	for sid, persona := range map[string]string{"ses_A": "alice", "ses_B": "bob"} {
		require.NoError(t, ss.Create(sid,
			session.Participant{AgentID: "human", Persona: "bob"},
			session.Participant{AgentID: sid, Persona: persona, Parent: "human"}, "", ""))
	}
	h := NewHandler(identity.NewLayeredStore(repo, global),
		attribute.NewStore(repo.Root(), attribute.Talents),
		attribute.NewStore(repo.Root(), attribute.Personalities),
		attribute.NewStore(repo.Root(), attribute.WritingStyles), ss)
	return h, root
}

func assertMetadataIdentity(t *testing.T, h *Handler, sid, handle string) {
	t.Helper()
	result, err := h.handleIdentity(context.Background(), metadataCall(t, sid, map[string]any{"method": "whoami"}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(t, result))
	var id identity.Identity
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &id))
	assert.Equal(t, handle, id.Handle)
	assert.Equal(t, "Repo "+handle, id.Name, "metadata caller must use the effective layered identity store")
}

func TestMetadataCaller_Interleaved(t *testing.T) {
	h, _ := metadataHandler(t)
	t.Setenv("ETHOS_SESSION", "ses_B")
	t.Setenv("ETHOS_AGENT_ID", "human")
	t.Setenv("USER", "bob")
	assertMetadataIdentity(t, h, "ses_A", "alice")
	assertMetadataIdentity(t, h, "ses_B", "bob")
	for _, tc := range []struct{ sid, persona string }{{"ses_A", "carol"}, {"ses_B", "alice"}} {
		result, err := h.handleSession(context.Background(), metadataCall(t, tc.sid, map[string]any{
			"method": "iam", "persona": tc.persona, "session_id": tc.sid, "agent_id": tc.sid,
		}))
		require.NoError(t, err)
		require.False(t, result.IsError, resultText(t, result))
	}
	assertMetadataIdentity(t, h, "ses_B", "alice")
	assertMetadataIdentity(t, h, "ses_A", "carol")
	for _, sid := range []string{"ses_A", "ses_B"} {
		result, err := h.handleSession(context.Background(), metadataCall(t, sid, map[string]any{"method": "roster"}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		var roster session.Roster
		require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &roster))
		assert.Equal(t, sid, roster.Session)
		require.Len(t, roster.Participants, 2)
		assert.Equal(t, "bob", roster.FindParticipant("human").Persona)
	}
	assert.Equal(t, "ses_B", os.Getenv("ETHOS_SESSION"))
	assert.Equal(t, "human", os.Getenv("ETHOS_AGENT_ID"))
}

func TestMetadataCaller_InvalidMetadata(t *testing.T) {
	for _, sid := range []any{nil, 123, true, map[string]any{}, "", " ", "ses_A ", "../ses_A", "a/b", `a\b`, ".", "..", "a\n", "a\x00"} {
		for _, method := range []string{"whoami", "roster", "iam"} {
			t.Run(method+"/"+stringifyMetadata(t, sid), func(t *testing.T) {
				h, _ := metadataHandler(t)
				req := metadataCall(t, sid, map[string]any{"method": method, "persona": "carol", "session_id": "ses_A"})
				var result *mcplib.CallToolResult
				var err error
				if method == "whoami" {
					result, err = h.handleIdentity(context.Background(), req)
				} else {
					result, err = h.handleSession(context.Background(), req)
				}
				require.NoError(t, err)
				assert.True(t, result.IsError)
				assert.Contains(t, resultText(t, result), opencodeSessionKey)
			})
		}
	}
}

func stringifyMetadata(t *testing.T, sid any) string {
	t.Helper()
	data, err := json.Marshal(sid)
	require.NoError(t, err)
	return string(data)
}

func TestMetadataCaller_InvalidRoster(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"missing roster", "", "not found"},
		{"corrupt YAML", "participants: [", "invalid roster"},
		{"empty roster", "{}", "does not match"},
		{"wrong session", "session: ses_B\nparticipants: [{agent_id: ses_A, persona: alice}]", "does not match"},
		{"missing exact participant", "session: ses_A\nparticipants: [{agent_id: human, persona: alice}]", "participant"},
		{"duplicate participant", "session: ses_A\nparticipants: [{agent_id: ses_A, persona: alice}, {agent_id: ses_A, persona: bob}]", "duplicated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, root := metadataHandler(t)
			path := filepath.Join(root, "sessions", "ses_A.yaml")
			if tc.content == "" {
				require.NoError(t, os.Remove(path))
			} else {
				require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
			}
			for _, method := range []string{"whoami", "roster", "iam"} {
				req := metadataCall(t, "ses_A", map[string]any{"method": method, "persona": "carol"})
				var result *mcplib.CallToolResult
				var err error
				if method == "whoami" {
					result, err = h.handleIdentity(context.Background(), req)
				} else {
					result, err = h.handleSession(context.Background(), req)
				}
				require.NoError(t, err)
				assert.True(t, result.IsError, method)
				assert.Contains(t, resultText(t, result), tc.want, method)
			}
			if tc.content != "" {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, tc.content, string(data))
			} else {
				_, err := os.Stat(path)
				assert.ErrorIs(t, err, os.ErrNotExist, "must not auto-create roster")
			}
		})
	}
}

func TestMetadataCaller_InvalidPersona(t *testing.T) {
	for _, persona := range []string{"", "missing", "../alice", "BadHandle"} {
		t.Run(persona, func(t *testing.T) {
			h, _ := metadataHandler(t)
			require.NoError(t, h.sessionStore.SetExistingPersona("ses_A", "ses_A", persona))
			result, err := h.handleIdentity(context.Background(), metadataCall(t, "ses_A", map[string]any{"method": "whoami"}))
			require.NoError(t, err)
			assert.True(t, result.IsError)
			assert.Contains(t, resultText(t, result), "persona")
			result, err = h.handleSession(context.Background(), metadataCall(t, "ses_A", map[string]any{"method": "iam", "persona": "carol"}))
			require.NoError(t, err)
			assert.True(t, result.IsError, "a dangling caller must fail rather than silently acquire another identity")
			result, err = h.handleSession(context.Background(), metadataCall(t, "ses_B", map[string]any{"method": "iam", "persona": persona}))
			require.NoError(t, err)
			assert.True(t, result.IsError)
			assertMetadataIdentity(t, h, "ses_B", "bob")
		})
	}
}

func TestMetadataCaller_InvalidIdentity(t *testing.T) {
	for _, content := range []string{"name: [", "{}", "name: Wrong\nhandle: bob\nkind: agent\n"} {
		t.Run(content, func(t *testing.T) {
			h, root := metadataHandler(t)
			require.NoError(t, os.WriteFile(filepath.Join(root, "repo", "identities", "alice.yaml"), []byte(content), 0o600))
			result, err := h.handleIdentity(context.Background(), metadataCall(t, "ses_A", map[string]any{"method": "whoami"}))
			require.NoError(t, err)
			assert.True(t, result.IsError, resultText(t, result))
			result, err = h.handleSession(context.Background(), metadataCall(t, "ses_B", map[string]any{"method": "iam", "persona": "alice"}))
			require.NoError(t, err)
			assert.True(t, result.IsError, resultText(t, result))
			assertMetadataIdentity(t, h, "ses_B", "bob")
		})
	}
}

func TestMetadataCaller_IamMismatch(t *testing.T) {
	for _, args := range []map[string]any{
		{"session_id": "ses_B"}, {"agent_id": "human"}, {"session_id": 12}, {"agent_id": 12},
	} {
		h, _ := metadataHandler(t)
		args["method"], args["persona"] = "iam", "carol"
		result, err := h.handleSession(context.Background(), metadataCall(t, "ses_A", args))
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assertMetadataIdentity(t, h, "ses_A", "alice")
		assertMetadataIdentity(t, h, "ses_B", "bob")
	}
}

func TestMetadataCaller_AdministrativeTarget(t *testing.T) {
	h, _ := metadataHandler(t)
	for _, args := range []map[string]any{
		{"method": "roster"}, {"method": "join", "agent_id": "new", "persona": "carol"}, {"method": "leave", "agent_id": "new"},
	} {
		args["session_id"] = "ses_B"
		result, err := h.handleSession(context.Background(), metadataCall(t, "ses_unregistered", args))
		require.NoError(t, err)
		assert.False(t, result.IsError, resultText(t, result))
	}
	assertMetadataIdentity(t, h, "ses_A", "alice")
	assertMetadataIdentity(t, h, "ses_B", "bob")
}

func TestMetadataCaller_UnrelatedMetaPreservesLegacy(t *testing.T) {
	h, _ := metadataHandler(t)
	t.Setenv("ETHOS_SESSION", "ses_A")
	t.Setenv("ETHOS_AGENT_ID", "ses_A")
	for _, method := range []string{"whoami", "roster", "iam"} {
		req := callTool(map[string]any{"method": method, "persona": "legacy-unregistered-persona"})
		req.Params.Meta = &mcplib.Meta{AdditionalFields: map[string]any{"other/key": "value"}}
		var result *mcplib.CallToolResult
		var err error
		if method == "whoami" {
			result, err = h.handleIdentity(context.Background(), req)
		} else {
			result, err = h.handleSession(context.Background(), req)
		}
		require.NoError(t, err)
		assert.False(t, result.IsError, resultText(t, result))
	}
	roster, err := h.sessionStore.Load("ses_A")
	require.NoError(t, err)
	assert.Equal(t, "legacy-unregistered-persona", roster.FindParticipant("ses_A").Persona)
}
