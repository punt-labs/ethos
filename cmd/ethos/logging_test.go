//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

func TestStdinWatchdogWritesNoDiagnosticsOrUsage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	oldArgs, oldContext := os.Args, rootCmd.Context()
	oldLogger, oldUsage, oldFinish := processLogger, processUsage, finishProcess
	t.Cleanup(func() {
		os.Args = oldArgs
		rootCmd.SetContext(oldContext)
		processLogger, processUsage, finishProcess = oldLogger, oldUsage, oldFinish
	})
	os.Args = []string{"ethos", "hook", "stdin-watchdog", "123"}
	startLogging()
	finishProcess(0, nil)
	finishProcess(1, errors.New("cancelled"))
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	require.Empty(t, entries, "watchdog startup and exit must not write logs or usage")
	require.Nil(t, processUsage)
}

func TestProcessLoggingRolesAndSingleCompletion(t *testing.T) {
	for _, tc := range []struct {
		args []string
		role string
		code int
	}{
		{[]string{"version"}, "cli", 0},
		{[]string{"mission", "create"}, "cli", 1},
		{[]string{"hook", "pre-tool-use"}, "hook", 0},
		{[]string{"hook", "private-content"}, "hook", 2},
		{[]string{"serve"}, "mcp", 0},
		{[]string{"ui"}, "ui", 0},
	} {
		t.Run(strings.Join(tc.args, "-"), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ETHOS_TELEMETRY", "off")
			oldArgs, oldContext := os.Args, rootCmd.Context()
			oldLogger, oldUsage, oldFinish := processLogger, processUsage, finishProcess
			t.Cleanup(func() {
				os.Args = oldArgs
				rootCmd.SetContext(oldContext)
				processLogger, processUsage, finishProcess = oldLogger, oldUsage, oldFinish
			})
			os.Args = append([]string{"ethos"}, tc.args...)
			startLogging()
			if tc.role == "mcp" || tc.role == "ui" {
				processLogger.Info("Server started")
				logServerStop(processLogger, time.Now(), nil)
				logServerStop(processLogger, time.Now(), fmt.Errorf("private wrapper: %w", errors.New("private-content")))
			}
			var err error
			if tc.code != 0 {
				err = fmt.Errorf("private wrapper: %w", errors.New("private-content"))
			}
			finishProcess(tc.code, err)
			finishProcess(tc.code, err)
			raw, err := os.ReadFile(filepath.Join(home, ".punt-labs", "ethos", "logs", "ethos.log"))
			require.NoError(t, err)
			log := string(raw)
			require.Contains(t, log, "ethos."+tc.role+":")
			require.NotContains(t, log, "private-content")
			require.NotContains(t, log, "private wrapper")
			if tc.role == "cli" || tc.role == "hook" {
				require.Equal(t, 1, strings.Count(log, "Invocation finished"))
				require.Contains(t, log, fmt.Sprintf("exit_code=%d", tc.code))
				if tc.code != 0 {
					require.Contains(t, log, "error_type=*errors.errorString")
				}
			}
		})
	}
}

func TestServerStartupFailureHasNoLifecycle(t *testing.T) {
	var sink bytes.Buffer
	oldLogger := processLogger
	processLogger = slog.New(slog.NewTextHandler(&sink, nil))
	t.Cleanup(func() { processLogger = oldLogger })
	t.Setenv("HOME", "")
	require.Error(t, runServeImpl(false))
	require.Empty(t, sink.String(), "a server that failed setup never started or stopped")
	oldRoots := cmdRoots.Load()
	blocked := &repoRoots{}
	blocked.CheckoutErr = errors.New("unavailable checkout")
	cmdRoots.Store(blocked)
	t.Cleanup(func() { cmdRoots.Store(oldRoots) })
	require.Error(t, runUI())
	require.Empty(t, sink.String(), "a UI that failed setup never started or stopped")
}

func TestToolOutcomeHooksValidateMetadata(t *testing.T) {
	oldUsage := processUsage
	processUsage = nil
	t.Cleanup(func() { processUsage = oldUsage })
	tool := &server.ServerTool{Tool: mcp.NewTool("identity", mcp.WithString("method", mcp.Enum("list")))}
	for _, tc := range []struct {
		name, action, outcome string
		err, toolError        bool
	}{
		{"identity", "list", "completed", false, false},
		{"identity", "list", "failed", false, true},
		{"identity", "list", "failed", true, false},
		{"identity", "private-content", "completed", false, false},
		{"private-content", "private-content", "failed", true, false},
	} {
		t.Run(fmt.Sprintf("%s-%s-%t-%t", tc.name, tc.action, tc.err, tc.toolError), func(t *testing.T) {
			var sink bytes.Buffer
			hooks := toolLoggingHooks(slog.New(slog.NewTextHandler(&sink, nil)), func(name string) *server.ServerTool {
				if name == "identity" {
					return tool
				}
				return nil
			})
			var req mcp.CallToolRequest
			req.Params.Name = tc.name
			req.Params.Arguments = map[string]any{"method": tc.action}
			ctx := context.Background()
			for _, hook := range hooks.OnBeforeCallTool {
				hook(ctx, nil, &req)
			}
			if tc.err {
				for _, hook := range hooks.OnError {
					hook(ctx, nil, mcp.MethodToolsCall, &req, errors.New("private-content"))
				}
			} else {
				for _, hook := range hooks.OnAfterCallTool {
					hook(ctx, nil, &req, &mcp.CallToolResult{IsError: tc.toolError})
				}
			}
			for _, hook := range hooks.OnError {
				hook(ctx, nil, mcp.MethodInitialize, "private-content", errors.New("private-content"))
			}
			log := sink.String()
			require.Equal(t, 1, strings.Count(log, "Tool call finished"))
			require.Contains(t, log, "outcome="+tc.outcome)
			require.Contains(t, log, "duration_ms=")
			require.NotContains(t, log, "private-content")
			if tc.action == "list" {
				require.Contains(t, log, "action=list")
			} else {
				require.NotContains(t, log, "action=")
			}
		})
	}
}
