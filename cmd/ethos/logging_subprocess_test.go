//go:build linux || darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func loggingProcess(t *testing.T, home, cwd string, args ...string) *exec.Cmd {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "the real CLI must build")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, ethosBinary, args...)
	cmd.Dir = cwd
	cmd.Env = withForcedNotUnderClaudeCode([]string{"HOME=" + home, "USER=logging-private-login", "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"})
	return cmd
}

func diagnosticLog(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".punt-labs", "ethos", "logs")
	path := filepath.Join(dir, "ethos.log")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	for p, mode := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		require.Equal(t, mode, info.Mode().Perm())
	}
	return string(b)
}

func TestSharedLoggingCLI(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			args := []string{"version"}
			if failed {
				args = append(args, "--private-token=ghp_abcdefghijklmnopqrstuvwxyz0123456789", filepath.Join(home, "private-content"))
			}
			cmd := loggingProcess(t, home, cwd, args...)
			if failed {
				require.Error(t, cmd.Run())
			} else {
				require.NoError(t, cmd.Run())
			}
			log := diagnosticLog(t, home)
			require.Equal(t, 1, strings.Count(log, "Invocation finished"), log)
			require.Regexp(t, `(?m)^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[INFO\] ethos.cli: Invocation finished`, log)
			require.Contains(t, log, "command=version")
			if failed {
				require.Contains(t, log, "outcome=failed")
				require.Contains(t, log, "exit_code=2")
				require.Contains(t, log, "error_type=")
			} else {
				require.Contains(t, log, "outcome=completed")
				require.Contains(t, log, "exit_code=0")
			}
			require.Contains(t, log, "duration_ms=")
			for _, private := range []string{home, cwd, "logging-private-login", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "private-content", "private-token"} {
				require.NotContains(t, log, private)
			}
		})
	}
}

func TestSharedLoggingHook(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cmd := loggingProcess(t, home, cwd, "hook", "format-output")
	cmd.Stdin = strings.NewReader(`{"tool_name":"unrelated","tool_response":{}}`)
	out, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolUse\",\"updatedMCPToolOutput\":\"{}\"}}\n", string(out), "hook JSON must remain unchanged")
	log := diagnosticLog(t, home)
	require.Contains(t, log, "[INFO] ethos.hook: Invocation finished command=hook format-output outcome=completed")
	require.Equal(t, 1, strings.Count(log, "Invocation finished"))
}

func TestSharedLoggingMCP(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cmd := loggingProcess(t, home, cwd, "serve")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	// A nil Stderr deliberately discards diagnostics from the real server.
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for _, request := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"logging-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"identity","arguments":{"method":"list"}}}`,
	} {
		_, err = stdin.Write([]byte(request + "\n"))
		require.NoError(t, err)
		require.True(t, scanner.Scan(), "server must answer each request: %v", scanner.Err())
		var response map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &response), scanner.Text())
		require.JSONEq(t, `"2.0"`, string(response["jsonrpc"]))
		require.NotContains(t, response, "error", scanner.Text())
	}
	require.NoError(t, stdin.Close())
	for scanner.Scan() {
		var response map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &response))
		require.JSONEq(t, `"2.0"`, string(response["jsonrpc"]))
	}
	require.NoError(t, scanner.Err())
	require.NoError(t, cmd.Wait())
	log := diagnosticLog(t, home)
	for _, text := range []string{"ethos.mcp: Server started", "ethos.mcp: Server stopped", "ethos.mcp: Tool call finished", "tool=identity", "action=list", "outcome=completed"} {
		require.Contains(t, log, text)
	}
	require.Equal(t, 1, strings.Count(log, "Tool call finished"))
	require.False(t, regexp.MustCompile(`(?m)^.*(logging-private-login|private-content).*$`).MatchString(log))
}

func TestSharedLoggingUnavailable(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".punt-labs"), []byte("blocked"), 0o600))
	cmd := loggingProcess(t, home, cwd, "version")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "ethos: private diagnostics unavailable\n", stderr.String())
}
