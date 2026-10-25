//go:build linux || darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// breakConfig makes the repo's ethos.yaml unusable in the named way and
// returns the path.
func breakConfig(t *testing.T, repo, kind string) string {
	t.Helper()
	cfg := filepath.Join(repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.RemoveAll(cfg))
	switch kind {
	case "malformed":
		require.NoError(t, os.WriteFile(cfg, []byte("agent: [unclosed\n"), 0o644))
	case "dangling":
		if err := os.Symlink(filepath.Join(repo, "nowhere.yaml"), cfg); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	default:
		t.Fatalf("unknown kind %q", kind)
	}
	return cfg
}

var configBreaks = []string{"malformed", "dangling"}

// A broken ethos.yaml does not kill a hook before it can say so: session-start
// tells the session and writes a repo_unresolved roster.
func TestCLI_BrokenConfigSessionStartSaysSo(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	for _, kind := range configBreaks {
		t.Run(kind, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			breakConfig(t, se.repo, kind)
			payload := `{"session_id":"cfg-s1","hook_event_name":"SessionStart"}`
			stdout, stderr, _ := runHookIn(t, se, se.repo, "session-start", payload)
			assert.Contains(t, stdout, "ethos.yaml", "the session is told which file: stderr=%s", stderr)
			roster, err := os.ReadFile(filepath.Join(se.home, ".punt-labs", "ethos", "sessions", "cfg-s1.yaml"))
			require.NoError(t, err)
			assert.Contains(t, string(roster), "repo_unresolved")
		})
	}
}

// subagent-start and pre-compact report the cause, in the session.
func TestCLI_BrokenConfigSubagentStartAndPreCompactReport(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	for _, kind := range configBreaks {
		t.Run(kind, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			runHookIn(t, se, se.repo, "session-start", `{"session_id":"cfg-s2","hook_event_name":"SessionStart"}`)
			cfg := breakConfig(t, se.repo, kind)
			stdout, stderr, _ := runHookIn(t, se, se.repo, "subagent-start",
				`{"session_id":"cfg-s2","agent_id":"a-1","agent_type":"bwk","hook_event_name":"SubagentStart"}`)
			assert.Contains(t, stdout, "cannot determine verifier gate", stderr)
			assert.Contains(t, stdout, filepath.Base(cfg), stderr)

			stdout, stderr, _ = runHookIn(t, se, se.repo, "pre-compact",
				`{"session_id":"cfg-s2","hook_event_name":"PreCompact"}`)
			assert.Contains(t, stdout+stderr, "could not be located", "pre-compact says so")
			assert.Contains(t, stdout+stderr, filepath.Base(cfg))
		})
	}
}

// doctor prints its table, with a failing Repository store row, instead of
// dying on the config.
func TestCLI_BrokenConfigDoctorPrintsItsTable(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	for _, kind := range configBreaks {
		t.Run(kind, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			cfg := breakConfig(t, se.repo, kind)
			c := exec.Command(ethosBinary, "doctor")
			c.Dir = se.repo
			c.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := c.CombinedOutput()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, string(out))
			var row string
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "Repository store") {
					row = line
				}
			}
			assert.Contains(t, row, "FAIL", string(out))
			assert.Contains(t, row, filepath.Base(cfg), string(out))
			assert.Contains(t, string(out), "Duplicate fields", "the rest of the table is printed")
		})
	}
}

// serve starts with the config broken, notes it on reads and refuses writes;
// and a running serve survives the config breaking mid-session.
func TestServeSurvivesABrokenConfig(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	t.Setenv("HOME", se.home)
	t.Chdir(se.repo)
	call := func(srv *server.MCPServer, tool, method string) *mcp.CallToolResult {
		t.Helper()
		st := srv.GetTool(tool)
		require.NotNil(t, st, tool)
		var req mcp.CallToolRequest
		req.Params.Name = tool
		req.Params.Arguments = map[string]any{"method": method}
		res, err := st.Handler(context.Background(), req)
		require.NoError(t, err)
		return res
	}
	build := func() *server.MCPServer {
		srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
		h := buildServeHandler(se.home, locateServeStore())
		h.RegisterTools(srv)
		return srv
	}

	// Healthy first: no note.
	assert.Len(t, call(build(), "identity", "list").Content, 1)

	// Breaking mid-session: the next build (as the relocation makes it) survives.
	for _, kind := range configBreaks {
		breakConfig(t, se.repo, kind)
		srv := build()
		res := call(srv, "identity", "list")
		require.Len(t, res.Content, 2, "a read carries the note")
		note, ok := res.Content[1].(mcp.TextContent)
		require.True(t, ok)
		assert.Contains(t, note.Text, "ethos.yaml")
		w := call(srv, "identity", "create")
		assert.True(t, w.IsError)
		assert.Contains(t, w.Content[0].(mcp.TextContent).Text, "refusing")
	}
}

// The MCP doctor tool, through serve, shows the failing Repository store row
// for a broken store, as the CLI does.
func TestServeDoctorShowsAFailingStoreRow(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	t.Setenv("HOME", se.home)
	t.Chdir(se.repo)
	cfg := breakConfig(t, se.repo, "malformed")
	srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
	buildServeHandler(se.home, locateServeStore()).RegisterTools(srv)
	var req mcp.CallToolRequest
	req.Params.Name = "doctor"
	res, err := srv.GetTool("doctor").Handler(context.Background(), req)
	require.NoError(t, err)
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			text += tc.Text + "\n"
		}
	}
	var row string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "Repository store") {
			row = line
		}
	}
	assert.Contains(t, row, "FAIL", text)
	assert.Contains(t, row, filepath.Base(cfg), text)
}

// doctor from a linked worktree whose main repository leaves core.bare unset,
// with a trace2 target in the user's git config: the store is found, not a
// failing Repository store row.
func TestCLI_DoctorLinkedWorktreeUnderTrace2(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	base := t.TempDir()
	main := filepath.Join(base, "main")
	require.NoError(t, os.MkdirAll(filepath.Join(main, ".punt-labs", "ethos"), 0o755))
	gitCmd(t, main, "init", "-q")
	gitCmd(t, main, "-c", "user.email=t@x", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "i")
	gitCmd(t, main, "config", "--unset", "core.bare")
	wt := filepath.Join(base, "wt")
	gitCmd(t, main, "worktree", "add", "-q", wt)
	cfg := filepath.Join(base, "gitconfig")
	require.NoError(t, os.WriteFile(cfg, []byte("[trace2]\n\tnormalTarget = 2\n"), 0o644))

	c := exec.Command(ethosBinary, "doctor")
	c.Dir = wt
	c.Env = append(withForcedNotUnderClaudeCode(se.env), "GIT_CONFIG_GLOBAL="+cfg)
	out, _ := c.CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Repository store") {
			t.Fatalf("a Repository store row means the store was not found: %s\n%s", line, out)
		}
	}
	assert.Contains(t, string(out), "Duplicate fields")
}
