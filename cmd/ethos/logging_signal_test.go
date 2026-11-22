//go:build linux || darwin

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSharedLoggingMCPTermination(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cmd := loggingProcess(t, home, cwd, "serve")
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill() })
	_, err = fmt.Fprintln(in, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	require.NoError(t, err)
	reader := bufio.NewScanner(out)
	require.True(t, reader.Scan())
	require.True(t, json.Valid(reader.Bytes()))
	_, err = fmt.Fprintln(in, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"identity","arguments":{"method":"list"}}}`)
	require.NoError(t, err)
	require.True(t, reader.Scan())
	require.True(t, json.Valid(reader.Bytes()))
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	err = cmd.Wait()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 1, exit.ExitCode(), "preserve ServeStdio cancellation semantics")
	log := diagnosticLog(t, home)
	require.Equal(t, 1, strings.Count(log, "Server stopped"), log)
	require.Contains(t, log, "reason=failed")
	assertUsageRow(t, home, "ethos.mcp.call", "tool", "action", "outcome", "duration_ms")
}

func TestSignalLoggingHelper(t *testing.T) {
	if os.Getenv("ETHOS_SIGNAL_TEST") != "1" {
		return
	}
	startLogging()
	fmt.Println("ready")
	time.Sleep(time.Hour)
}

func TestSharedLoggingCLITermination(t *testing.T) {
	for _, received := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(received.String(), func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			cmd := loggingProcess(t, home, cwd)
			self, err := os.Executable()
			require.NoError(t, err)
			cmd.Path, cmd.Args = self, []string{self, "-test.run=^TestSignalLoggingHelper$"}
			cmd.Env = append(cmd.Env, "ETHOS_SIGNAL_TEST=1")
			out, err := cmd.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, cmd.Start())
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			reader := bufio.NewScanner(out)
			require.True(t, reader.Scan())
			require.Equal(t, "ready", reader.Text())
			require.NoError(t, cmd.Process.Signal(received))
			err = cmd.Wait()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			status := exit.Sys().(syscall.WaitStatus)
			require.True(t, status.Signaled(), "preserve default signal exit semantics")
			require.Equal(t, received, status.Signal())
			log := diagnosticLog(t, home)
			require.Equal(t, 1, strings.Count(log, "Invocation finished"), log)
			require.Contains(t, log, fmt.Sprintf("exit_code=%d", 128+int(received)))
			require.Contains(t, log, "outcome=failed")
			assertUsageOutcome(t, home, "ethos.cli.invoke", "failed", "command", "outcome", "exit_code", "duration_ms")
		})
	}
}
