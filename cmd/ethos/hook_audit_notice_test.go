//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
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

// A repository append cannot bypass the shared stream lock. If it cannot be
// taken, the tool still proceeds but the session is told its audit was dropped.
func TestCLI_HookAuditLogStreamLockFailureIsReported(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	const sid = "unlocked1"
	policy, err := layout.NewRepository(se.repo, se.repo, layout.RecordBlame)
	require.NoError(t, err)
	stream := audit.Stream{Namespace: audit.SessionNS, Session: sid}
	lock, err := policy.LockPath("audit-stream", stream.Key())
	require.NoError(t, err)
	// A directory where the shared stream lock goes makes acquisition fail.
	require.NoError(t, os.MkdirAll(lock, 0o700))
	payload := `{"session_id":"` + sid + `","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	stdout, stderr, code := runHookIn(t, se, se.repo, "audit-log", payload)
	assert.Equal(t, 0, code, stderr)
	system, context := auditNotice(t, stdout)
	assert.Contains(t, system, "audit entry not written")
	assert.Contains(t, system, lock)
	assert.Equal(t, system, context)
	assert.NoFileExists(t, stream.LivePath(policy), "no unlocked repository append")
}
