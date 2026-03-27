package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHandlerWithTeams(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	s := identity.NewStore(dir)
	root := s.Root()
	rs := role.NewLayeredStore("", root)
	ts := team.NewLayeredStore("", root)

	// Create identities and roles for team tests.
	require.NoError(t, s.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))
	require.NoError(t, s.Save(&identity.Identity{
		Name: "Bob", Handle: "bob", Kind: "agent",
	}))
	require.NoError(t, rs.Save(&role.Role{Name: "dev"}))
	require.NoError(t, rs.Save(&role.Role{Name: "lead"}))

	return NewHandlerWithOptions(s,
		attribute.NewStore(root, attribute.Talents),
		attribute.NewStore(root, attribute.Personalities),
		attribute.NewStore(root, attribute.WritingStyles),
		WithRoleStore(rs),
		WithTeamStore(ts),
	)
}

func TestHandleTeam_CreateAndShow(t *testing.T) {
	h := testHandlerWithTeams(t)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method":       "create",
		"name":         "eng",
		"repositories": []interface{}{"punt-labs/ethos"},
		"members": []interface{}{
			map[string]interface{}{"identity": "alice", "role": "dev"},
		},
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := resultText(t, result)
	assert.Contains(t, text, "eng")
	assert.Contains(t, text, "alice")

	result, err = h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "show",
		"name":   "eng",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "punt-labs/ethos")
}

func TestHandleTeam_List(t *testing.T) {
	h := testHandlerWithTeams(t)

	// Create two teams.
	_, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "alpha",
		"members": []interface{}{
			map[string]interface{}{"identity": "alice", "role": "dev"},
		},
	}))
	require.NoError(t, err)

	_, err = h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "beta",
		"members": []interface{}{
			map[string]interface{}{"identity": "bob", "role": "lead"},
		},
	}))
	require.NoError(t, err)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	text := resultText(t, result)
	assert.Contains(t, text, "alpha")
	assert.Contains(t, text, "beta")
}

func TestHandleTeam_Delete(t *testing.T) {
	h := testHandlerWithTeams(t)

	_, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "eng",
		"members": []interface{}{
			map[string]interface{}{"identity": "alice", "role": "dev"},
		},
	}))
	require.NoError(t, err)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "delete", "name": "eng",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "Deleted")
}

func TestHandleTeam_AddMember(t *testing.T) {
	h := testHandlerWithTeams(t)

	_, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "eng",
		"members": []interface{}{
			map[string]interface{}{"identity": "alice", "role": "dev"},
		},
	}))
	require.NoError(t, err)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method":   "add_member",
		"name":     "eng",
		"identity": "bob",
		"role":     "lead",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	// Verify via show.
	result, err = h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "show", "name": "eng",
	}))
	require.NoError(t, err)
	var loaded team.Team
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &loaded))
	assert.Len(t, loaded.Members, 2)
}

func TestHandleTeam_RemoveMember(t *testing.T) {
	h := testHandlerWithTeams(t)

	_, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "eng",
		"members": []interface{}{
			map[string]interface{}{"identity": "alice", "role": "dev"},
			map[string]interface{}{"identity": "bob", "role": "lead"},
		},
	}))
	require.NoError(t, err)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method":   "remove_member",
		"name":     "eng",
		"identity": "bob",
		"role":     "lead",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestHandleTeam_RemoveMember_LastMember(t *testing.T) {
	h := testHandlerWithTeams(t)

	_, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "eng",
		"members": []interface{}{
			map[string]interface{}{"identity": "alice", "role": "dev"},
		},
	}))
	require.NoError(t, err)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method":   "remove_member",
		"name":     "eng",
		"identity": "alice",
		"role":     "dev",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "last member")
}

func TestHandleTeam_AddCollab(t *testing.T) {
	h := testHandlerWithTeams(t)

	_, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "eng",
		"members": []interface{}{
			map[string]interface{}{"identity": "alice", "role": "dev"},
			map[string]interface{}{"identity": "bob", "role": "lead"},
		},
	}))
	require.NoError(t, err)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method":      "add_collab",
		"name":        "eng",
		"from":        "dev",
		"to":          "lead",
		"collab_type": "reports_to",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	// Verify via show.
	result, err = h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "show", "name": "eng",
	}))
	require.NoError(t, err)
	var loaded team.Team
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &loaded))
	assert.Len(t, loaded.Collaborations, 1)
	assert.Equal(t, "reports_to", loaded.Collaborations[0].Type)
}

func TestHandleTeam_AddCollab_SelfCollab(t *testing.T) {
	h := testHandlerWithTeams(t)

	_, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "eng",
		"members": []interface{}{
			map[string]interface{}{"identity": "alice", "role": "dev"},
		},
	}))
	require.NoError(t, err)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method":      "add_collab",
		"name":        "eng",
		"from":        "dev",
		"to":          "dev",
		"collab_type": "reports_to",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "self-collaboration")
}

func TestHandleTeam_CreateMissingName(t *testing.T) {
	h := testHandlerWithTeams(t)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "name is required")
}

func TestHandleTeam_CreateNoMembers(t *testing.T) {
	h := testHandlerWithTeams(t)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "create",
		"name":   "eng",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "at least one member")
}

func TestHandleTeam_UnknownMethod(t *testing.T) {
	h := testHandlerWithTeams(t)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "bogus",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "unknown method")
}

func TestHandleTeam_ForRepo(t *testing.T) {
	h := testHandlerWithTeams(t)

	// Create a team with a repository.
	_, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method":       "create",
		"name":         "eng",
		"repositories": []interface{}{"punt-labs/ethos"},
		"members": []interface{}{
			map[string]interface{}{"identity": "alice", "role": "dev"},
		},
	}))
	require.NoError(t, err)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "for_repo",
		"repo":   "punt-labs/ethos",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := resultText(t, result)
	assert.Contains(t, text, "eng")
	assert.Contains(t, text, "alice")
}

func TestHandleTeam_ForRepo_NoMatch(t *testing.T) {
	h := testHandlerWithTeams(t)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "for_repo",
		"repo":   "punt-labs/nonexistent",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "[]")
}

func TestHandleTeam_ForRepo_NoRepoArg(t *testing.T) {
	// Outside any git repo, for_repo with no repo arg should fail gracefully.
	// Use the system temp dir (not TMPDIR which may point inside a repo).
	t.Setenv("TMPDIR", "")
	tmp, err := os.MkdirTemp("", "ethos-no-git-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmp) })

	origDir, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmp))

	// Prevent git from finding a repo above the temp dir.
	t.Setenv("GIT_CEILING_DIRECTORIES", tmp)

	h := testHandlerWithTeams(t)
	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "for_repo",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "repo is required")
}

func TestHandleTeam_ShowNotFound(t *testing.T) {
	h := testHandlerWithTeams(t)

	result, err := h.handleTeam(context.Background(), callTool(map[string]interface{}{
		"method": "show",
		"name":   "nonexistent",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "not found")
}
