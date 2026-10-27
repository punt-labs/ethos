//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// danglingStoreEnv is a repo whose .punt-labs/ethos is a dangling symlink,
// which no call to locate the repository notices: only the validated roots do.
func danglingStoreEnv(t *testing.T) (*cliSubprocessEnv, string) {
	t.Helper()
	se := setupCLISubprocessEnv(t)
	store := filepath.Join(se.repo, ".punt-labs", "ethos")
	require.NoError(t, os.RemoveAll(store))
	if err := os.Symlink(filepath.Join(se.repo, "nowhere"), store); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return se, store
}

// pre-tool-use denies an Agent spawn when the store is broken, naming the
// cause.
func TestCLI_DanglingStoreDeniesAgentSpawn(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, store := danglingStoreEnv(t)
	payload := `{"session_id":"dangling-s1","tool_name":"Agent","tool_input":{"subagent_type":"bwk","prompt":"p"}}`
	stdout, stderr, code := runHookIn(t, se, se.repo, "pre-tool-use", payload)
	assert.Equal(t, 0, code, stderr)
	var res struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	assert.Equal(t, "deny", res.HookSpecificOutput.PermissionDecision, stdout)
	assert.Contains(t, res.HookSpecificOutput.PermissionDecisionReason, store)
}

// session-start tells the session, and records no persona from the global
// store and a roster that says its repository was not resolved.
func TestCLI_DanglingStoreSessionStartSaysSo(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, store := danglingStoreEnv(t)
	payload := `{"session_id":"dangling-s2","hook_event_name":"SessionStart"}`
	stdout, stderr, _ := runHookIn(t, se, se.repo, "session-start", payload)
	assert.Contains(t, stdout, store, "the session is told which path is broken: stderr=%s", stderr)
	roster, err := os.ReadFile(filepath.Join(se.home, ".punt-labs", "ethos", "sessions", "dangling-s2.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(roster), "repo_unresolved")
	assert.NotContains(t, string(roster), "persona: test-agent", "no persona from the global store")
}

// doctor shows a failing Repository store row.
func TestCLI_DanglingStoreDoctorFails(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, store := danglingStoreEnv(t)
	c := exec.Command(ethosBinary, "doctor")
	c.Dir = se.repo
	c.Env = withForcedNotUnderClaudeCode(se.env)
	out, err := c.CombinedOutput()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, string(out))
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Repository store") {
			found = true
			assert.Contains(t, line, "FAIL")
			assert.Contains(t, line, store)
		}
	}
	assert.True(t, found, "a Repository store row: %s", out)
}

// subagent-start cannot decide the verifier gate against a broken store, and
// says so in the session rather than on stderr alone.
func TestCLI_DanglingStoreSubagentStartSaysSo(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, store := danglingStoreEnv(t)
	payload := `{"session_id":"dangling-s3","agent_id":"a-1","agent_type":"bwk","hook_event_name":"SubagentStart"}`
	stdout, stderr, _ := runHookIn(t, se, se.repo, "subagent-start", payload)
	assert.Contains(t, stdout, "cannot determine verifier gate", stderr)
	assert.Contains(t, stdout, store, stderr)
}
