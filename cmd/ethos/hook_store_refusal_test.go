//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runHookIn runs `ethos hook <name>` in dir with payload on stdin.
func runHookIn(t *testing.T, se *cliSubprocessEnv, dir, name, payload string, extraEnv ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(ethosBinary, "hook", name)
	cmd.Dir = dir
	cmd.Env = append(withForcedNotUnderClaudeCode(se.env), extraEnv...)
	cmd.Stdin = strings.NewReader(payload)
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return outb.String(), errb.String(), ee.ExitCode()
	}
	require.NoError(t, err)
	return outb.String(), errb.String(), 0
}

// pathsNaming returns every path under root whose name contains s.
func pathsNaming(t *testing.T, root, s string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner is not what this looks for
		}
		if strings.Contains(d.Name(), s) {
			found = append(found, p)
		}
		return nil
	})
	require.NoError(t, err)
	return found
}

// audit-log in a checkout that cannot be located skips the write with the
// reason on stderr: never the legacy global sessions directory, never the
// enclosing repository. It never blocks the tool call.
func TestCLI_HookAuditLogSkipsWhenCheckoutUnlocatable(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	const sid = "auditskip1"
	payload := `{"session_id":"` + sid + `","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	stdout, stderr, code := runHookIn(t, se, inner, "audit-log", payload)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stderr, "audit-log")
	assert.Contains(t, stderr, filepath.Join(inner, ".git"))
	assert.Contains(t, stderr, "not written")
	// The session sees the dropped entry, not only the hook's stderr log.
	var res struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	assert.Contains(t, res.SystemMessage, "audit entry not written")
	assert.Contains(t, res.SystemMessage, filepath.Join(inner, ".git"))
	assert.Equal(t, res.SystemMessage, res.HookSpecificOutput.AdditionalContext)
	assert.Empty(t, pathsNaming(t, se.home, sid), "nothing in the global store")
	assert.Empty(t, pathsNaming(t, se.repo, sid), "nothing in the enclosing repository")
}

// subagent-start with a declared mission refuses when the store cannot be
// located: the verifier gate cannot run against the global store. Without a
// declared mission the spawn proceeds, and the session sees that the gate
// could not be decided (systemMessage), not a stderr line alone.
func TestCLI_HookSubagentStartRefusesGateWhenStoreUnlocatable(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	payload := `{"agent_id":"agent-1","agent_type":"bwk","session_id":"gate-s1"}`

	_, stderr, code := runHookIn(t, se, inner, "subagent-start", payload, "MISSION_ID=m-2026-01-01-001")
	assert.NotEqual(t, 0, code, stderr)
	assert.Contains(t, stderr, "verifier")
	assert.Contains(t, stderr, filepath.Join(inner, ".git"))

	stdout, stderr, code := runHookIn(t, se, inner, "subagent-start", payload)
	assert.Equal(t, 0, code, stderr)
	var res struct {
		SystemMessage string `json:"systemMessage"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	assert.Contains(t, res.SystemMessage, "cannot determine verifier gate: repository store not located")
	assert.Contains(t, res.SystemMessage, filepath.Join(inner, ".git"))
}

// pre-tool-use under a declared mission, in a checkout whose store cannot be
// located, denies with the refusal reason: never a contract read from the
// global mission store. The same holds for an Agent spawn, whose
// attribution (pending dispatch, claim, inheritance) reads the store.
func TestCLI_HookPreToolUseDeniesWhenStoreUnlocatable(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	cases := map[string]struct {
		payload string
		env     []string
	}{
		"Write under MISSION_ID": {
			payload: `{"session_id":"pre-s1","tool_name":"Write","tool_input":{"file_path":"x","content":"y"}}`,
			env:     []string{"MISSION_ID=m-2026-01-01-001"},
		},
		"Agent under MISSION_ID": {
			payload: `{"session_id":"pre-s1","tool_name":"Agent","tool_input":{"subagent_type":"bwk","prompt":"p"}}`,
			env:     []string{"MISSION_ID=m-2026-01-01-001"},
		},
		"Agent with no session_id": {
			payload: `{"tool_name":"Agent","tool_input":{"subagent_type":"bwk","prompt":"p"}}`,
		},
		"Agent without a mission": {
			payload: `{"session_id":"pre-s1","tool_name":"Agent","tool_input":{"subagent_type":"bwk","prompt":"p"}}`,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := runHookIn(t, se, inner, "pre-tool-use", c.payload, c.env...)
			assert.Equal(t, 0, code, stderr)
			var res struct {
				HookSpecificOutput struct {
					PermissionDecision       string `json:"permissionDecision"`
					PermissionDecisionReason string `json:"permissionDecisionReason"`
				} `json:"hookSpecificOutput"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
			assert.Equal(t, "deny", res.HookSpecificOutput.PermissionDecision, stdout)
			reason := res.HookSpecificOutput.PermissionDecisionReason
			assert.Contains(t, reason, filepath.Join(inner, ".git"))
			// The reason says what to fix, not only what failed.
			assert.Contains(t, reason, "repair or remove")
			assert.Contains(t, reason, "ETHOS_REPO_ROOT")
		})
	}
}
