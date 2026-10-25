//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditNotice decodes the session notice audit-log prints on stdout.
func auditNotice(t *testing.T, stdout string) (system, context string) {
	t.Helper()
	var res struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	return res.SystemMessage, res.HookSpecificOutput.AdditionalContext
}

// A payload with no session_id is a dropped entry: the session is told,
// as for every other loss. The tool call is never blocked.
func TestCLI_HookAuditLogNoSessionIDIsADrop(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	payload := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	stdout, stderr, code := runHookIn(t, se, se.repo, "audit-log", payload)
	assert.Equal(t, 0, code, stderr)
	system, context := auditNotice(t, stdout)
	assert.Contains(t, system, "audit entry not written")
	assert.Contains(t, system, "session_id")
	assert.Equal(t, system, context)
}

// When the session lock cannot be taken the entry is still written,
// unlocked, and the session is told so.
func TestCLI_HookAuditLogUnlockedWriteIsReported(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	const sid = "unlocked1"
	// A directory where the lock file goes: the lock cannot be opened, the
	// live audit file beside it can.
	require.NoError(t, os.MkdirAll(audit.LiveAuditLockPath(se.repo, sid), 0o700))
	payload := `{"session_id":"` + sid + `","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	stdout, stderr, code := runHookIn(t, se, se.repo, "audit-log", payload)
	assert.Equal(t, 0, code, stderr)
	system, context := auditNotice(t, stdout)
	assert.Contains(t, system, "without the live lock")
	assert.Equal(t, system, context)
	data, err := os.ReadFile(audit.LiveAuditPath(se.repo, sid))
	require.NoError(t, err, "the entry is written")
	assert.Contains(t, string(data), `"tool":"Bash"`)
}
