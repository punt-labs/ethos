package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/adr"
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

// fullServer registers every tool, every optional store configured, on a
// handler given opts.
func fullServer(t *testing.T, opts ...Option) (*server.MCPServer, *identity.Store) {
	t.Helper()
	s := identity.NewStore(t.TempDir())
	root := s.Root()
	all := append([]Option{
		WithRoleStore(role.NewLayeredStore(t.TempDir(), t.TempDir())),
		WithTeamStore(team.NewLayeredStore(t.TempDir(), t.TempDir())),
		WithMissionStore(mission.NewStore(t.TempDir())),
		WithADRStore(adr.NewStore(t.TempDir())),
		WithVendorRunner(func(vendor.Options) (*vendor.Plan, error) {
			t.Fatal("vendor must not run when the store cannot be located")
			return nil, nil
		}),
	}, opts...)
	h := NewHandlerWithOptions(s,
		attribute.NewStore(root, attribute.Talents),
		attribute.NewStore(root, attribute.Personalities),
		attribute.NewStore(root, attribute.WritingStyles),
		all...,
	)
	srv := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	h.RegisterTools(srv)
	return srv, s
}

// guardedServer registers every tool on a handler that was told the repository
// store could not be located.
func guardedServer(t *testing.T) *server.MCPServer {
	t.Helper()
	srv, _ := fullServer(t, WithStoreError(errNoStore))
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

// methodEnum returns the values of a tool's "method" enum, or [""] for a tool
// with no method parameter (all its calls are one operation).
func methodEnum(t *testing.T, tool mcplib.Tool) []string {
	t.Helper()
	prop, ok := tool.InputSchema.Properties["method"].(map[string]any)
	if !ok {
		return []string{""}
	}
	switch e := prop["enum"].(type) {
	case []string:
		return e
	case []any:
		out := make([]string, 0, len(e))
		for _, v := range e {
			s, ok := v.(string)
			require.True(t, ok, "%s method enum value %v", tool.Name, v)
			out = append(out, s)
		}
		return out
	}
	t.Fatalf("%s has a method parameter with no enum", tool.Name)
	return nil
}

// Every method of every registered tool is classified: a read or a write of a
// guarded tool, or a tool that never touches the repository store. The
// classification has no stale entries.
func TestEveryToolMethodIsClassified(t *testing.T) {
	srv := guardedServer(t)
	seen := map[string]bool{}
	for name, st := range srv.ListTools() {
		seen[name] = true
		if unguardedTools[name] {
			assert.NotContains(t, storeReads, name, "%s is unguarded and guarded", name)
			assert.NotContains(t, storeWrites, name, "%s is unguarded and guarded", name)
			continue
		}
		enum := methodEnum(t, st.Tool)
		for _, m := range enum {
			r, w := slices.Contains(storeReads[name], m), slices.Contains(storeWrites[name], m)
			assert.True(t, r != w, "%s %q must be exactly one of read or write (read=%v write=%v)", name, m, r, w)
		}
		for _, m := range append(append([]string{}, storeReads[name]...), storeWrites[name]...) {
			assert.Contains(t, enum, m, "%s %q is classified but not a method", name, m)
		}
	}
	for name := range storeReads {
		assert.True(t, seen[name], "storeReads names unregistered tool %s", name)
	}
	for name := range storeWrites {
		assert.True(t, seen[name], "storeWrites names unregistered tool %s", name)
	}
	for name := range unguardedTools {
		assert.True(t, seen[name], "unguardedTools names unregistered tool %s", name)
	}
}

// With the store unlocated, every method that is not a declared read refuses
// with the reason — derived from each tool's own method enum, so a method
// added later is refused until it is classified a read.
func TestOnlyReadsRunWhenStoreUnlocatable(t *testing.T) {
	srv := guardedServer(t)
	for name, st := range srv.ListTools() {
		if unguardedTools[name] {
			continue
		}
		for _, method := range methodEnum(t, st.Tool) {
			if slices.Contains(storeReads[name], method) {
				continue
			}
			t.Run(name+" "+method, func(t *testing.T) {
				result := callRegistered(t, srv, name, method)
				assert.True(t, result.IsError)
				assert.Contains(t, resultText(t, result), errNoStore.Error())
				assert.Contains(t, resultText(t, result), "refusing")
			})
		}
	}

	t.Run("unknown method", func(t *testing.T) {
		result := callRegistered(t, srv, "identity", "frobnicate")
		assert.True(t, result.IsError)
		assert.Contains(t, resultText(t, result), "refusing")
	})
}

// A read still runs. Its own content is untouched — element 0 still parses as
// the tool's JSON — and the note rides as a separate text block.
func TestReadsNoteWhenStoreUnlocatable(t *testing.T) {
	srv := guardedServer(t)
	for name, methods := range storeReads {
		for _, method := range methods {
			if strictTools[name] {
				continue // refuses instead: TestIdentityMethodsRefuseWhenStoreUnlocatable
			}
			if method == "whoami" || method == "get" || method == "show" || method == "results" ||
				method == "reflections" || method == "log" || method == "for_repo" {
				continue // needs arguments; list covers the shape
			}
			t.Run(name+" "+method, func(t *testing.T) {
				result := callRegistered(t, srv, name, method)
				require.Len(t, result.Content, 2, "the note is a second content block")
				first := resultText(t, result)
				assert.NotContains(t, first, "could not be located")
				if !result.IsError {
					var v any
					assert.NoError(t, json.Unmarshal([]byte(first), &v), "element 0 stays the tool's JSON: %s", first)
				}
				note, ok := result.Content[1].(mcplib.TextContent)
				require.True(t, ok)
				assert.Contains(t, note.Text, "repository store could not be located")
				assert.Contains(t, note.Text, errNoStore.Error())
			})
		}
	}
}

// Every identity method refuses while the store cannot be used, reads
// included: an identity answered from the global store alone is the wrong
// answer, as the CLI's identity commands refuse it.
func TestIdentityMethodsRefuseWhenStoreUnlocatable(t *testing.T) {
	srv, s := fullServer(t, WithStoreError(errNoStore))
	require.NoError(t, s.Save(&identity.Identity{Name: "Mal Reynolds", Handle: "mal", Kind: "human"}))
	for _, method := range []string{"list", "get", "whoami"} {
		result := callRegistered(t, srv, "identity", method)
		assert.True(t, result.IsError, method)
		assert.Contains(t, resultText(t, result), errNoStore.Error(), method)
		assert.NotContains(t, resultText(t, result), "mal", "no identity is returned")
	}
}

// With a located store nothing changes.
func TestGuardIsInertWithoutAStoreError(t *testing.T) {
	srv, _ := fullServer(t)
	result := callRegistered(t, srv, "identity", "list")
	assert.Len(t, result.Content, 1)
	assert.NotContains(t, resultText(t, result), "could not be located")
}

// Every tool is registered through addTool, which guards it unless it is
// declared unguarded: no other file calls AddTool on the server.
func TestEveryToolRegisteredThroughAddTool(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	bare := regexp.MustCompile(`\.AddTools?\(`)
	var offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, line := range strings.Split(string(data), "\n") {
			if !bare.MatchString(line) {
				continue
			}
			if f == "store_guard.go" && strings.Contains(line, "s.AddTool(tool, ") {
				continue // addTool itself
			}
			offenders = append(offenders, fmt.Sprintf("%s:%d: %s", f, i+1, strings.TrimSpace(line)))
		}
	}
	sort.Strings(offenders)
	assert.Empty(t, offenders, "register tools with h.addTool, never AddTool directly")
}
