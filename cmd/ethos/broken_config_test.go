//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/spf13/cobra"
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
			assert.Contains(t, stdout+stderr, "the repository configuration could not be read", "pre-compact says so, in the words for a config")
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
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "Human identity") || strings.Contains(line, "Default agent") {
					assert.Contains(t, line, "WARN", "a store-only check that could not look is not a PASS: %s", line)
					assert.Contains(t, line, "not checked")
				}
			}
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
	assert.Len(t, call(build(), "talent", "list").Content, 1)

	// Breaking mid-session: the next build (as the relocation makes it) survives.
	for _, kind := range configBreaks {
		breakConfig(t, se.repo, kind)
		srv := build()
		res := call(srv, "talent", "list")
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

// The global-store warning names the located error, not "no git repository".
func TestWarnIfGlobalFallbackNamesTheLocatedError(t *testing.T) {
	loc := &repoRoots{Located: resolve.Located{ConfigErr: errors.New("parsing repo config /r/.punt-labs/ethos.yaml: boom")}, Global: true}
	cmdRoots.Store(loc)
	t.Cleanup(func() { cmdRoots.Store(nil) })
	out := testhelpers.CaptureStderr(t, func() { warnIfGlobalFallback("") })
	assert.Contains(t, out, "could not be located")
	assert.Contains(t, out, "boom")
	assert.NotContains(t, out, "no git repository found")

	cmdRoots.Store(&repoRoots{Global: true})
	out = testhelpers.CaptureStderr(t, func() { warnIfGlobalFallback("") })
	assert.Contains(t, out, "no git repository found")
}

// The MCP team for_repo call from r/sub, where r/.git is a symlink to nothing:
// the error says so; the origin of some enclosing repository is not read.
func TestServeTeamForRepoUnderADanglingAncestorGit(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	t.Setenv("HOME", se.home)
	base := t.TempDir()
	r := filepath.Join(base, "r")
	sub := filepath.Join(r, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.Symlink(filepath.Join(r, "nowhere"), filepath.Join(r, ".git")))
	t.Setenv("GIT_CEILING_DIRECTORIES", base)
	t.Chdir(sub)

	srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
	buildServeHandler(se.home, locateServeStore()).RegisterTools(srv)
	var req mcp.CallToolRequest
	req.Params.Name = "team"
	req.Params.Arguments = map[string]any{"method": "for_repo"}
	res, err := srv.GetTool("team").Handler(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	text := res.Content[0].(mcp.TextContent).Text
	assert.Contains(t, text, filepath.Join(r, ".git"))
	assert.Contains(t, text, "symlink to nothing")
}

// ethos audit seal run directly applies the whole refusal: a broken store
// config blocks the commit (exit 2), it does not seal against a guess.
func TestAuditSealRefusesABrokenStore(t *testing.T) {
	repo := t.TempDir()
	testhelpers.MakeGitDir(t, repo)
	cmdRoots.Store(&repoRoots{Located: resolve.Located{
		Store: repo, Checkout: repo, ConfigErr: errors.New("parsing repo config /r/.punt-labs/ethos.yaml: boom"),
	}, Global: true})
	t.Cleanup(func() { cmdRoots.Store(nil) })
	var out, errb bytes.Buffer
	err := runAuditSeal(&out, &errb)
	assert.ErrorIs(t, err, failClosed{})
	assert.Contains(t, errb.String(), "boom")
}

// An unreadable git config is an error on every identity path: the CLI, the
// session-start hook and the MCP whoami report it, and none falls through to
// $USER as if no git user were set.
func TestUnreadableGitConfigIsReportedEverywhere(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	se := setupCLISubprocessEnv(t)
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(cfg, []byte("[user]\n\tname = hidden\n"), 0o644))
	require.NoError(t, os.Chmod(cfg, 0o000))
	t.Cleanup(func() { _ = os.Chmod(cfg, 0o644) })
	env := func() []string {
		var out []string
		for _, kv := range withForcedNotUnderClaudeCode(se.env) {
			if !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
				out = append(out, kv)
			}
		}
		return append(out, "GIT_CONFIG_GLOBAL="+cfg)
	}

	if ethosBinary != "" {
		c := exec.Command(ethosBinary, "whoami")
		c.Dir = se.repo
		c.Env = env()
		out, err := c.CombinedOutput()
		require.Error(t, err, string(out))
		assert.Contains(t, string(out), cfg)

		hse := *se
		hse.env = env()
		stdout, stderr, _ := runHookIn(t, &hse, se.repo, "session-start", `{"session_id":"gc-s1","hook_event_name":"SessionStart"}`)
		assert.Contains(t, stdout+stderr, cfg)
		roster, err := os.ReadFile(filepath.Join(se.home, ".punt-labs", "ethos", "sessions", "gc-s1.yaml"))
		require.NoError(t, err)
		assert.NotContains(t, string(roster), "persona: "+os.Getenv("USER"), "no persona from $USER")
	}

	t.Setenv("HOME", se.home)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Chdir(se.repo)
	srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
	buildServeHandler(se.home, locateServeStore()).RegisterTools(srv)
	var req mcp.CallToolRequest
	req.Params.Name = "identity"
	req.Params.Arguments = map[string]any{"method": "whoami"}
	res, err := srv.GetTool("identity").Handler(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].(mcp.TextContent).Text, cfg)
}

// The store note and the doctor row say "configuration could not be read" when
// only the config failed, once, never "X; X".
func TestConfigOnlyFailureIsWordedAsAConfigFailure(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	for _, kind := range configBreaks {
		t.Run(kind, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			cfg := breakConfig(t, se.repo, kind)
			stdout, _, _ := runHookIn(t, se, se.repo, "session-start", `{"session_id":"cw-s1","hook_event_name":"SessionStart"}`)
			assert.Contains(t, stdout, "the repository configuration could not be read")
			assert.NotContains(t, stdout, "could not be located")
			roster, err := os.ReadFile(filepath.Join(se.home, ".punt-labs", "ethos", "sessions", "cw-s1.yaml"))
			require.NoError(t, err)
			assert.Equal(t, 1, strings.Count(string(roster), filepath.Base(cfg)), "the cause is said once: %s", roster)

			c := exec.Command(ethosBinary, "doctor")
			c.Dir = se.repo
			c.Env = withForcedNotUnderClaudeCode(se.env)
			out, _ := c.CombinedOutput()
			assert.Contains(t, string(out), "Repository store                      FAIL  the repository configuration could not be read")
			if kind == "dangling" {
				assert.NotContains(t, string(out), "dangling symlink: it is a dangling symlink")
				assert.NotContains(t, string(out), "dangling config symlink")
			}
		})
	}
}

// A missing active bundle is told on every surface, in words that do not say
// the global stores were all that was used: the repository layer was.
func TestBundleErrorIsToldEverywhere(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	cfg := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.RemoveAll(cfg))
	require.NoError(t, os.WriteFile(cfg, []byte("active_bundle: no-such-bundle\n"), 0o644))
	const what = "the repository's bundle or policy configuration is unusable"

	// session-start: in systemMessage, not only stderr.
	stdout, _, _ := runHookIn(t, se, se.repo, "session-start", `{"session_id":"bd-s1","hook_event_name":"SessionStart"}`)
	var res struct {
		SystemMessage string `json:"systemMessage"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	assert.Contains(t, res.SystemMessage, what)
	assert.Contains(t, res.SystemMessage, "no-such-bundle")
	assert.NotContains(t, res.SystemMessage, "global stores only")

	// doctor: a failing row.
	c := exec.Command(ethosBinary, "doctor")
	c.Dir = se.repo
	c.Env = withForcedNotUnderClaudeCode(se.env)
	out, err := c.CombinedOutput()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, string(out))
	assert.Contains(t, string(out), "Repository configuration")
	assert.Contains(t, string(out), "no-such-bundle")
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Human identity") || strings.Contains(line, "Default agent") {
			assert.Contains(t, line, "WARN", "an identity row under a policy issue did not check the repository: %s", line)
			assert.Contains(t, line, "not checked")
		}
	}

	// serve: a note on reads, writes refused; MCP whoami errors.
	t.Setenv("HOME", se.home)
	t.Chdir(se.repo)
	srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
	buildServeHandler(se.home, locateServeStore()).RegisterTools(srv)
	call := func(tool, method string) *mcp.CallToolResult {
		var req mcp.CallToolRequest
		req.Params.Name = tool
		req.Params.Arguments = map[string]any{"method": method}
		r, err := srv.GetTool(tool).Handler(context.Background(), req)
		require.NoError(t, err)
		return r
	}
	// Every guarded read and write refuses with the issue's description, as the
	// CLI does; nothing is answered from a layer the repository did not choose.
	read := call("talent", "list")
	require.True(t, read.IsError)
	refusal := read.Content[0].(mcp.TextContent).Text
	assert.Contains(t, refusal, what)
	assert.Contains(t, refusal, "no-such-bundle")
	assert.NotContains(t, refusal, "repository layer is used")
	doc := call("doctor", "")
	assert.False(t, doc.IsError, "doctor still runs and reports")
	assert.Contains(t, doc.Content[0].(mcp.TextContent).Text, "Repository configuration")
	assert.True(t, call("talent", "create").IsError, "a write refuses")
	who := call("identity", "whoami")
	assert.True(t, who.IsError)
	assert.Contains(t, who.Content[0].(mcp.TextContent).Text, "no-such-bundle")
}

// The mission-store stderr line words a config failure as one.
func TestWarnIfGlobalFallbackWordsAConfigFailure(t *testing.T) {
	cmdRoots.Store(&repoRoots{Located: resolve.Located{ConfigErr: &resolve.ConfigError{
		Err: errors.New("boom"), What: "the repository configuration could not be read"}}, Global: true})
	t.Cleanup(func() { cmdRoots.Store(nil) })
	out := testhelpers.CaptureStderr(t, func() { warnIfGlobalFallback("") })
	assert.Contains(t, out, "the repository configuration could not be read (boom)")
	assert.NotContains(t, out, "could not be located")
}

// A bundle or policy issue refuses every store command with exit 1, naming the
// cause; `mission list` does not print "No missions found." from a layer the
// repository did not choose.
func TestConfigIssueRefusesStoreCommands(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	for name, yaml := range map[string]string{
		"missing bundle":     "active_bundle: nosuchbundle\n",
		"unknown resolution": "resolution: repo_only\n",
	} {
		t.Run(name, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			cfg := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
			require.NoError(t, os.RemoveAll(cfg))
			require.NoError(t, os.WriteFile(cfg, []byte(yaml), 0o644))
			c := exec.Command(ethosBinary, "mission", "list")
			c.Dir = se.repo
			c.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := c.CombinedOutput()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, string(out))
			assert.Equal(t, 1, ee.ExitCode(), string(out))
			assert.Contains(t, string(out), "bundle or policy configuration is unusable")
			assert.NotContains(t, string(out), "No missions found")
		})
	}
}

// An unknown `resolution:` value is a policy failure: session-start refuses the
// persona (no human identity either) and says nothing was answered, as the CLI
// and the MCP refuse; it does not answer from the layers left.
func TestSessionStartRefusesAPersonaOnAPolicyIssue(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	cfg := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.RemoveAll(cfg))
	require.NoError(t, os.WriteFile(cfg, []byte("agent: test-agent\nresolution: repo_only\n"), 0o644))
	stdout, _, _ := runHookIn(t, se, se.repo, "session-start", `{"session_id":"po-s1","hook_event_name":"SessionStart"}`)
	var res struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	assert.Contains(t, res.SystemMessage, "unknown resolution")
	assert.Contains(t, res.SystemMessage, "global layer would have been consulted")
	assert.NotContains(t, res.HookSpecificOutput.AdditionalContext, "Active identity", "no identity was answered")
	assert.NotContains(t, res.HookSpecificOutput.AdditionalContext, "Test Agent")
}

var policyBreaks = map[string]string{
	"unknown resolution": "agent: test-agent\nresolution: repo_only\n",
	"missing bundle":     "agent: test-agent\nactive_bundle: missingbundle\n",
}

// On a policy issue subagent-start and pre-compact inject no persona, record
// none, and tell the session why, as session-start does. The agent identity
// exists only in the global store.
func TestSubagentStartAndPreCompactRefuseAPersonaOnAPolicyIssue(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	for name, yaml := range policyBreaks {
		t.Run(name, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			ids := filepath.Join(se.home, ".punt-labs", "ethos", "identities")
			require.NoError(t, os.MkdirAll(ids, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(ids, "ghost-agent.yaml"),
				[]byte("name: Ghost Agent\nhandle: ghost-agent\nkind: agent\n"), 0o644))

			// A roster written while the config was healthy, with a primary persona.
			runHookIn(t, se, se.repo, "session-start", `{"session_id":"pol-s1","hook_event_name":"SessionStart"}`)
			cfg := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
			require.NoError(t, os.RemoveAll(cfg))
			require.NoError(t, os.WriteFile(cfg, []byte(yaml), 0o644))

			stdout, stderr, _ := runHookIn(t, se, se.repo, "subagent-start",
				`{"session_id":"pol-s1","agent_id":"a-9","agent_type":"ghost-agent","hook_event_name":"SubagentStart"}`)
			assert.Contains(t, stdout, "no persona or identity was answered", stderr)
			assert.NotContains(t, stdout, "Ghost Agent", "no persona from the global store")
			roster, err := os.ReadFile(filepath.Join(se.home, ".punt-labs", "ethos", "sessions", "pol-s1.yaml"))
			require.NoError(t, err)
			assert.NotContains(t, string(roster), "persona: ghost-agent", "no persona recorded (the agent_type is the caller's own string)")

			stdout, stderr, _ = runHookIn(t, se, se.repo, "pre-compact", `{"session_id":"pol-s1","hook_event_name":"PreCompact"}`)
			assert.Contains(t, stdout, "no persona or identity was answered", stderr)
			assert.NotContains(t, stdout, "Test Agent", "no persona is re-injected")
		})
	}
}

// With the store or its configuration unreadable, subagent-start and pre-compact
// answer no persona: a different identity of the same handle in the global store
// is not the repository's. Before hookIssue, configIssue() was nil while the
// store failed, so both hooks answered the global identity.
func TestHooksRefuseAGlobalPersonaWhenTheConfigCannotBeRead(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	breaks := map[string]func(t *testing.T, cfg string){
		"malformed": func(t *testing.T, cfg string) {
			require.NoError(t, os.WriteFile(cfg, []byte("agent: [unclosed\n"), 0o644))
		},
		"dangling": func(t *testing.T, cfg string) {
			require.NoError(t, os.Remove(cfg))
			require.NoError(t, os.Symlink(cfg+".nowhere", cfg))
		},
		"mode 000": func(t *testing.T, cfg string) {
			if os.Geteuid() == 0 {
				t.Skip("root ignores permissions")
			}
			require.NoError(t, os.Chmod(cfg, 0o000))
			t.Cleanup(func() { _ = os.Chmod(cfg, 0o644) })
		},
	}
	for name, brk := range breaks {
		t.Run(name, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			repoIDs := filepath.Join(se.repo, ".punt-labs", "ethos", "identities")
			globalIDs := filepath.Join(se.home, ".punt-labs", "ethos", "identities")
			require.NoError(t, os.MkdirAll(globalIDs, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(repoIDs, "agentx.yaml"), []byte("name: Repo Agent X\nhandle: agentx\nkind: agent\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(globalIDs, "agentx.yaml"), []byte("name: Global Agent X\nhandle: agentx\nkind: agent\n"), 0o644))
			cfg := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
			require.NoError(t, os.WriteFile(cfg, []byte("agent: agentx\n"), 0o644))
			runHookIn(t, se, se.repo, "session-start", `{"session_id":"gp-s1","hook_event_name":"SessionStart"}`)
			brk(t, cfg)

			stdout, stderr, _ := runHookIn(t, se, se.repo, "subagent-start",
				`{"session_id":"gp-s1","agent_id":"a-7","agent_type":"agentx","hook_event_name":"SubagentStart"}`)
			assert.NotContains(t, stdout, "Global Agent X", stderr)
			assert.Contains(t, stdout, "persona", "the session is told why: "+stdout)
			stdout, stderr, _ = runHookIn(t, se, se.repo, "pre-compact", `{"session_id":"gp-s1","hook_event_name":"PreCompact"}`)
			assert.NotContains(t, stdout, "Global Agent X", stderr)
			assert.NotContains(t, stdout, "Repo Agent X", "no persona at all")
			assert.Contains(t, stdout, "persona", stdout)
		})
	}
}

// With git installed, a .git git cannot use (mode 000, or its HEAD, objects or
// refs at mode 000) makes git say "not a git repository" or answer for an
// enclosing repository. ethos does not read that as "not in a repository":
// identity get answers neither the global identity nor silently.
func TestUnreadableDotGitIsNotOutsideARepository(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	for _, part := range []string{"", "HEAD", "objects", "refs"} {
		name := part
		if name == "" {
			name = ".git itself"
		}
		t.Run(name, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			ids := filepath.Join(se.home, ".punt-labs", "ethos", "identities")
			require.NoError(t, os.MkdirAll(ids, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(ids, "globalonly.yaml"), []byte("name: Global Only\nhandle: globalonly\nkind: human\n"), 0o644))
			dot := filepath.Join(se.repo, ".git")
			p := filepath.Join(dot, part)
			require.NoError(t, os.Chmod(p, 0o000))
			t.Cleanup(func() { _ = os.Chmod(p, 0o755) })

			c := exec.Command(ethosBinary, "identity", "get", "globalonly")
			c.Dir = se.repo
			c.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := c.CombinedOutput()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, string(out))
			assert.Equal(t, 2, ee.ExitCode(), string(out))
			assert.NotContains(t, string(out), "Global Only")
			assert.Contains(t, string(out), dot)
			// The per-class refusal, so a missing bundle or a missing identity
			// cannot satisfy this: the unusable part is named.
			unusable := part
			if part == "" {
				unusable = "HEAD" // a mode-000 .git cannot be searched, so HEAD is the first to fail
			}
			want := "its " + unusable + " cannot be used"
			assert.Contains(t, string(out), want)
		})
	}
}

// Every store command refuses under an unusable bundle configuration, not only
// the ones a test happened to name: walk the command tree, run each runnable
// command of the store-refusing class (but the ones that repair the
// configuration) with dummy arguments, and require exit 1 naming the cause.
func TestEveryStoreCommandRefusesUnderAConfigIssue(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	cfg := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.RemoveAll(cfg))
	require.NoError(t, os.WriteFile(cfg, []byte("active_bundle: nosuchbundle\n"), 0o644))

	rootCmd.InitDefaultHelpCmd()
	var checked []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		path := c.CommandPath()
		if storeUses[path] != storeRefuses || repairsConfig[path] || !c.Runnable() || slices.Contains(cobraCommands, path) {
			return
		}
		var args []string
		for i := 0; i < 6 && c.Args != nil && c.Args(c, args) != nil; i++ {
			args = append(args, "x")
		}
		argv := append(strings.Fields(strings.TrimPrefix(path, rootCmd.Name()+" ")), args...)
		t.Run(path, func(t *testing.T) {
			cmd := exec.Command(ethosBinary, argv...)
			cmd.Dir = se.repo
			cmd.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, "%v: %s", argv, out)
			assert.Equal(t, 1, ee.ExitCode(), "%v: %s", argv, out)
			assert.Contains(t, string(out), "bundle or policy configuration is unusable", "%v", argv)
		})
		checked = append(checked, path)
	}
	walk(rootCmd)
	assert.Greater(t, len(checked), 30, "the walk found the store commands: %v", checked)
}
