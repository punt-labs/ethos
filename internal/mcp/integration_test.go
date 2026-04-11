//go:build linux || darwin

package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var ethosBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ethos-mcp-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir: %v\n", err)
		os.Exit(1)
	}

	bin := filepath.Join(dir, "ethos")
	wd, _ := os.Getwd()
	root := filepath.Join(wd, "..", "..")

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/ethos")
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "go build failed: %v\n", err)
	} else {
		ethosBinary = bin
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// mcpTestEnv holds the filesystem layout for an MCP integration test.
type mcpTestEnv struct {
	home string
	repo string
	env  []string
}

// setupMCPTestEnv creates the filesystem layout the ethos server needs:
// global identity store, repo with git init and config.
func setupMCPTestEnv(t *testing.T) *mcpTestEnv {
	t.Helper()

	home := t.TempDir()
	repo := t.TempDir()

	// Global identity store.
	globalEthos := filepath.Join(home, ".punt-labs", "ethos")
	globalIDs := filepath.Join(globalEthos, "identities")
	require.NoError(t, os.MkdirAll(globalIDs, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(globalEthos, "sessions"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(globalEthos, "talents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(globalEthos, "personalities"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(globalEthos, "writing-styles"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(globalEthos, "roles"), 0o755))

	idData, err := yaml.Marshal(map[string]interface{}{
		"name":   "Test Agent",
		"handle": "test-agent",
		"kind":   "agent",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(globalIDs, "test-agent.yaml"), idData, 0o644))

	// Repo: git init so FindRepoRoot stops here.
	repoEthos := filepath.Join(repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(filepath.Join(repoEthos, "identities"), 0o755))

	gitEnv := []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	gitInit := exec.Command("git", "init", repo)
	gitInit.Env = gitEnv
	out, gitErr := gitInit.CombinedOutput()
	require.NoError(t, gitErr, "git init failed: %s", out)

	// Repo config.
	cfgData, err := yaml.Marshal(map[string]string{"agent": "test-agent"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), cfgData, 0o644))

	env := []string{
		"HOME=" + home,
		"USER=test-agent",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}

	return &mcpTestEnv{home: home, repo: repo, env: env}
}

// startMCPClient spawns ethos serve and returns an initialized MCP client.
func startMCPClient(t *testing.T, env *mcpTestEnv) *client.Client {
	t.Helper()

	// Use WithCommandFunc to set CWD on the subprocess so
	// FindRepoRoot discovers the fake repo.
	cmdFunc := func(ctx context.Context, command string, envVars []string, args []string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Dir = env.repo
		cmd.Env = envVars
		return cmd, nil
	}

	c, err := client.NewStdioMCPClientWithOptions(
		ethosBinary,
		env.env,
		[]string{"serve"},
		transport.WithCommandFunc(cmdFunc),
	)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.Initialize(ctx, mcplib.InitializeRequest{
		Params: mcplib.InitializeParams{
			ProtocolVersion: mcplib.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcplib.Implementation{
				Name:    "ethos-test",
				Version: "0.0.0",
			},
		},
	})
	require.NoError(t, err)
	return c
}

// callTool is a helper that calls a tool and returns the result.
func callTool(t *testing.T, c *client.Client, name string, args map[string]interface{}) *mcplib.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := c.CallTool(ctx, mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	})
	require.NoError(t, err)
	return result
}

// resultText extracts the text from a CallToolResult.
func resultText(t *testing.T, result *mcplib.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content, "expected non-empty content")
	tc, ok := result.Content[0].(mcplib.TextContent)
	require.True(t, ok, "expected TextContent, got %T", result.Content[0])
	return tc.Text
}

func TestMCP_ToolsList(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := c.ListTools(ctx, mcplib.ListToolsRequest{})
	require.NoError(t, err)

	names := make(map[string]bool)
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}

	for _, want := range []string{"identity", "ext", "session", "doctor"} {
		assert.True(t, names[want], "tools/list should include %q", want)
	}
}

func TestMCP_Identity_Whoami(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	result := callTool(t, c, "identity", map[string]interface{}{
		"method": "whoami",
	})
	assert.False(t, result.IsError, "whoami should not be an error")

	text := resultText(t, result)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &parsed), "response should be valid JSON")
	assert.Equal(t, "test-agent", parsed["handle"])
}

func TestMCP_Identity_List(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	result := callTool(t, c, "identity", map[string]interface{}{
		"method": "list",
	})
	assert.False(t, result.IsError, "list should not be an error")

	text := resultText(t, result)
	var entries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &entries), "response should be valid JSON array")

	found := false
	for _, e := range entries {
		if e["handle"] == "test-agent" {
			found = true
			break
		}
	}
	assert.True(t, found, "list should contain test-agent")
}

func TestMCP_Identity_Get(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	result := callTool(t, c, "identity", map[string]interface{}{
		"method": "get",
		"handle": "test-agent",
	})
	assert.False(t, result.IsError, "get should not be an error")

	text := resultText(t, result)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))
	assert.Equal(t, "test-agent", parsed["handle"])
	assert.Equal(t, "Test Agent", parsed["name"])
}

func TestMCP_Identity_Create(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	// Create a new identity.
	result := callTool(t, c, "identity", map[string]interface{}{
		"method": "create",
		"name":   "River Tam",
		"handle": "river",
		"kind":   "human",
	})
	assert.False(t, result.IsError, "create should not be an error: %s", resultText(t, result))

	// Verify via get.
	getResult := callTool(t, c, "identity", map[string]interface{}{
		"method": "get",
		"handle": "river",
	})
	assert.False(t, getResult.IsError, "get after create should not be an error")

	text := resultText(t, getResult)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))
	assert.Equal(t, "river", parsed["handle"])
	assert.Equal(t, "River Tam", parsed["name"])
}

func TestMCP_Ext_SetGetDel(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	// Set.
	setResult := callTool(t, c, "ext", map[string]interface{}{
		"method":    "set",
		"handle":    "test-agent",
		"namespace": "biff",
		"key":       "tty",
		"value":     "test-session",
	})
	assert.False(t, setResult.IsError, "ext set should not be an error: %s", resultText(t, setResult))

	// Get.
	getResult := callTool(t, c, "ext", map[string]interface{}{
		"method":    "get",
		"handle":    "test-agent",
		"namespace": "biff",
		"key":       "tty",
	})
	assert.False(t, getResult.IsError, "ext get should not be an error")
	text := resultText(t, getResult)
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &got))
	assert.Equal(t, "test-session", got["tty"])

	// Del.
	delResult := callTool(t, c, "ext", map[string]interface{}{
		"method":    "del",
		"handle":    "test-agent",
		"namespace": "biff",
		"key":       "tty",
	})
	assert.False(t, delResult.IsError, "ext del should not be an error")

	// Get after del should error.
	getAfter := callTool(t, c, "ext", map[string]interface{}{
		"method":    "get",
		"handle":    "test-agent",
		"namespace": "biff",
		"key":       "tty",
	})
	assert.True(t, getAfter.IsError, "ext get after del should be an error")
}

func TestMCP_Doctor(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	result := callTool(t, c, "doctor", map[string]interface{}{})
	assert.False(t, result.IsError, "doctor should not be an error")

	text := resultText(t, result)
	assert.Contains(t, text, "checks")
	assert.Contains(t, text, "passed")
}

func TestMCP_Identity_UnknownMethod(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	result := callTool(t, c, "identity", map[string]interface{}{
		"method": "bogus",
	})
	assert.True(t, result.IsError, "unknown method should be an error")
	text := resultText(t, result)
	assert.Contains(t, text, "unknown method")
}

func TestMCP_Attribute_List(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	result := callTool(t, c, "talent", map[string]interface{}{
		"method": "list",
	})
	assert.False(t, result.IsError, "talent list should not be an error: %s", resultText(t, result))
}

func TestMCP_Role_List(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	env := setupMCPTestEnv(t)
	c := startMCPClient(t, env)

	result := callTool(t, c, "role", map[string]interface{}{
		"method": "list",
	})
	assert.False(t, result.IsError, "role list should not be an error: %s", resultText(t, result))
}
