package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/punt-labs/ethos/v4/internal/vendor"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errNoStore = errors.New("examining /x/.git: permission denied")

// guardedServer registers every tool on a handler that was told the repository
// store could not be located.
func guardedServer(t *testing.T) *server.MCPServer {
	t.Helper()
	s := identity.NewStore(t.TempDir())
	root := s.Root()
	h := NewHandlerWithOptions(s,
		attribute.NewStore(root, attribute.Talents),
		attribute.NewStore(root, attribute.Personalities),
		attribute.NewStore(root, attribute.WritingStyles),
		WithRoleStore(role.NewLayeredStore(t.TempDir(), t.TempDir())),
		WithTeamStore(team.NewLayeredStore(t.TempDir(), t.TempDir())),
		WithMissionStore(mission.NewStore(t.TempDir())),
		WithVendorRunner(func(vendor.Options) (*vendor.Plan, error) {
			t.Fatal("vendor must not run when the store cannot be located")
			return nil, nil
		}),
		WithStoreError(errNoStore),
	)
	srv := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	h.RegisterTools(srv)
	return srv
}

func callRegistered(t *testing.T, srv *server.MCPServer, tool, method string) *mcplib.CallToolResult {
	t.Helper()
	st := srv.GetTool(tool)
	require.NotNil(t, st, tool)
	args := map[string]interface{}{}
	if method != "" {
		args["method"] = method
	}
	result, err := st.Handler(context.Background(), callTool(args))
	require.NoError(t, err)
	return result
}

// Every tool method that writes the repository store refuses, with the reason,
// when the store could not be located: the global store is the wrong place.
func TestWritesRefuseWhenStoreUnlocatable(t *testing.T) {
	srv := guardedServer(t)
	writes := map[string][]string{
		"identity":      {"create"},
		"ext":           {"set", "del"},
		"talent":        {"create", "delete", "add", "remove", "set"},
		"personality":   {"create", "delete", "set"},
		"writing_style": {"create", "delete", "set"},
		"role":          {"create", "delete"},
		"team":          {"create", "delete", "add_member", "remove_member", "add_collab"},
		"mission":       {"create", "close", "abandon", "reflect", "advance", "result", "correct"},
		"vendor":        {""},
	}
	for tool, methods := range writes {
		for _, method := range methods {
			t.Run(tool+" "+method, func(t *testing.T) {
				result := callRegistered(t, srv, tool, method)
				assert.True(t, result.IsError)
				assert.Contains(t, resultText(t, result), errNoStore.Error())
				assert.Contains(t, resultText(t, result), "refusing")
			})
		}
	}
}

// A read still runs but says the store was not located, so its answer is not
// mistaken for the repository's.
func TestReadsNoteWhenStoreUnlocatable(t *testing.T) {
	srv := guardedServer(t)
	reads := map[string][]string{
		"identity": {"list"},
		"talent":   {"list"},
		"role":     {"list"},
		"team":     {"list"},
		"mission":  {"list"},
	}
	for tool, methods := range reads {
		for _, method := range methods {
			t.Run(tool+" "+method, func(t *testing.T) {
				result := callRegistered(t, srv, tool, method)
				text := resultText(t, result)
				assert.Contains(t, text, "repository store could not be located")
				assert.Contains(t, text, errNoStore.Error())
			})
		}
	}
}

// With a located store nothing changes.
func TestGuardIsInertWithoutAStoreError(t *testing.T) {
	h := testHandler(t)
	srv := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	h.RegisterTools(srv)
	result := callRegistered(t, srv, "identity", "list")
	assert.NotContains(t, resultText(t, result), "could not be located")
}
