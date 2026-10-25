//go:build linux || darwin

package main

import (
	"bytes"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runHookIn runs `ethos hook <name>` in dir with payload on stdin.
func runHookIn(t *testing.T, se *cliSubprocessEnv, dir, name, payload string, extraEnv ...string) (stderr string, code int) {
	t.Helper()
	cmd := exec.Command(ethosBinary, "hook", name)
	cmd.Dir = dir
	cmd.Env = append(withForcedNotUnderClaudeCode(se.env), extraEnv...)
	cmd.Stdin = strings.NewReader(payload)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return errb.String(), ee.ExitCode()
	}
	require.NoError(t, err)
	return errb.String(), 0
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
	stderr, code := runHookIn(t, se, inner, "audit-log", payload)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stderr, "audit-log")
	assert.Contains(t, stderr, filepath.Join(inner, ".git"))
	assert.Contains(t, stderr, "not written")
	assert.Empty(t, pathsNaming(t, se.home, sid), "nothing in the global store")
	assert.Empty(t, pathsNaming(t, se.repo, sid), "nothing in the enclosing repository")
}

// subagent-start with a declared mission refuses when the store cannot be
// located: the verifier gate cannot run against the global store. Without a
// declared mission there is no gate, and the spawn proceeds.
func TestCLI_HookSubagentStartRefusesGateWhenStoreUnlocatable(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	payload := `{"agent_id":"agent-1","agent_type":"bwk","session_id":"gate-s1"}`

	stderr, code := runHookIn(t, se, inner, "subagent-start", payload, "MISSION_ID=m-2026-01-01-001")
	assert.NotEqual(t, 0, code, stderr)
	assert.Contains(t, stderr, "verifier")
	assert.Contains(t, stderr, filepath.Join(inner, ".git"))

	stderr, code = runHookIn(t, se, inner, "subagent-start", payload)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stderr, "verifier gate not evaluated")
}
