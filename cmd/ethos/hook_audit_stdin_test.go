//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runHookOpenPipe runs `ethos hook <name>` with payload fed through an inherited
// pipe whose write end is left open, the shape Claude Code produces: the child
// never sees EOF and must terminate on a complete JSON object or a timeout. It
// returns once the process exits or fails the test if it hangs.
func runHookOpenPipe(t *testing.T, se *cliSubprocessEnv, name, payload string) (stdout, stderr string, code int) {
	t.Helper()
	return runHookOpenPipeWith(t, se, name, func(w *os.File) { _, _ = w.Write([]byte(payload)) })
}

// runHookOpenPipeWith is runHookOpenPipe with the writer supplied by the
// caller, so a test can pause between pieces of the payload.
func runHookOpenPipeWith(t *testing.T, se *cliSubprocessEnv, name string, write func(w *os.File)) (stdout, stderr string, code int) {
	t.Helper()
	rFd, wFd, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	defer rFd.Close()
	defer wFd.Close()

	cmd := exec.Command(ethosBinary, "hook", name)
	cmd.Stdin = rFd
	cmd.Dir = se.repo
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	require.NoError(t, cmd.Start())

	// Write after the child starts so a payload larger than the OS pipe
	// buffer (~64KiB) does not block: the child drains it as it arrives. The
	// write end is left open — an open pipe with no EOF, the Claude Code
	// shape — so the child must terminate on a complete JSON object, not EOF.
	// A trailing EPIPE once the child exits is expected and ignored.
	go write(wFd)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		if ee, ok := waitErr.(*exec.ExitError); ok {
			return outb.String(), errb.String(), ee.ExitCode()
		}
		require.NoError(t, waitErr)
		return outb.String(), errb.String(), 0
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("hook %s hung on an open pipe: stderr %s", name, errb.String())
		return "", "", -1
	}
}

// A PostToolUse payload larger than one 64KiB read — every Edit of a large
// file — must be read in full, not truncated to 65536 bytes. Against the old
// single-Read fallback the payload became invalid JSON and audit-log printed
// "invalid JSON input (65536 bytes)"; the entry was never written.
func TestCLI_HookAuditLogLargePayloadNotTruncated(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	const sid = "large-audit-1"
	big := strings.Repeat("x", 200_000)
	payload, err := json.Marshal(map[string]any{
		"session_id": sid,
		"tool_name":  "Edit",
		"tool_input": map[string]any{"new_string": big},
	})
	require.NoError(t, err)
	require.Greater(t, len(payload), 65536)

	stdout, stderr, code := runHookOpenPipe(t, se, "audit-log", string(payload))
	assert.Equal(t, 0, code, stderr)
	assert.NotContains(t, stdout, "invalid JSON input", "payload must not be truncated")
	assert.NotContains(t, stdout, "65536 bytes", "payload must not be truncated at the 64KiB read boundary")
	assert.NotContains(t, stderr, "invalid JSON input")

	data, readErr := os.ReadFile(audit.LiveAuditPath(se.repo, sid))
	require.NoError(t, readErr, "the audit entry is written")
	assert.Contains(t, string(data), `"tool":"Edit"`)
}

// Claude Code can pause between the pieces of a large payload for longer than a
// second. The drain must wait for the rest, not record a truncated entry.
func TestCLI_HookAuditLogStalledPayloadStillRecorded(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	const sid = "stalled-audit-1"
	payload, err := json.Marshal(map[string]any{
		"session_id": sid,
		"tool_name":  "Edit",
		"tool_input": map[string]any{"new_string": strings.Repeat("x", 100_000)},
	})
	require.NoError(t, err)
	half := len(payload) / 2

	stdout, stderr, code := runHookOpenPipeWith(t, se, "audit-log", func(w *os.File) {
		_, _ = w.Write(payload[:half])
		time.Sleep(2 * time.Second)
		_, _ = w.Write(payload[half:])
	})
	assert.Equal(t, 0, code, stderr)
	assert.NotContains(t, stdout, "timed out")
	assert.NotContains(t, stderr, "timed out")

	data, readErr := os.ReadFile(audit.LiveAuditPath(se.repo, sid))
	require.NoError(t, readErr, "the audit entry is written")
	assert.Contains(t, string(data), `"tool":"Edit"`)
}
