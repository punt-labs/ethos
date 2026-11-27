package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/require"
)

func TestHandoffApprovalLeavesCheckoutUnchanged(t *testing.T) {
	testHandoffApprovalTTY(t, false)
}

func TestHandoffAgentApprovalLeavesCheckoutUnchanged(t *testing.T) {
	testHandoffApprovalTTY(t, true)
}

func testHandoffApprovalTTY(t *testing.T, agentOverride bool) {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	runGit(t, root, "init", "-q")
	files := map[string]string{
		".gitignore":                                     ".punt-labs/ethos/sessions/\n.punt-labs/local/\n",
		".punt-labs/ethos.yaml":                          "checks: project\n",
		".punt-labs/ethos/checks/project.yaml":           "name: project\nchecks:\n  - {name: live, stage: handoff, files: all, run: [/usr/bin/true]}\n",
		".punt-labs/ethos/checks/snapshots/project.yaml": "checks:\n  live:\n    profile: linux-worktree-v1\n    isolation: live-verified\n    epoch: proof\n    sources: {mode: worktree}\n",
	}
	if agentOverride {
		files[".punt-labs/ethos.yaml"] = "checks: none\n"
		files[".punt-labs/ethos/identities/implementer.yaml"] = "name: Implementer\nhandle: implementer\nkind: agent\nchecks: project\n"
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "legacy checkout")
	status := func() string {
		t.Helper()
		out, err := testhelpers.GitCommand("-C", root, "--no-optional-locks", "status", "--porcelain").CombinedOutput()
		require.NoError(t, err, "%s", out)
		return string(out)
	}
	before := status()
	require.Empty(t, before)
	env := testhelpers.CoverEnv([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	args := []string{"-q", "/dev/null", ethosBinary, "handoff", "approve-live", "project"}
	if agentOverride {
		args = append(args, "--agent", "implementer")
	}
	approve := exec.CommandContext(ctx, "/usr/bin/script", args...)
	approve.Dir, approve.Env = root, env
	in, err := approve.StdinPipe()
	require.NoError(t, err)
	out, err := approve.StdoutPipe()
	require.NoError(t, err)
	approve.Stderr = approve.Stdout
	require.NoError(t, approve.Start())
	t.Cleanup(func() { _ = approve.Wait() })
	reader := bufio.NewReader(out)
	var prompt strings.Builder
	for !strings.Contains(prompt.String(), "Type yes to approve: ") {
		b, err := reader.ReadByte()
		require.NoError(t, err, "%s", prompt.String())
		prompt.WriteByte(b)
	}
	_, err = io.WriteString(in, "yes\n")
	require.NoError(t, err)
	approved, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, approve.Wait(), "%s", approved)
	require.Contains(t, string(approved), "Approved project")
	require.Equal(t, before, status(), "approval changed checkout")

	stop := exec.CommandContext(ctx, ethosBinary, "hook", "subagent-stop")
	stop.Dir, stop.Env = root, env
	stop.Stdin = strings.NewReader(`{"session_id":"fixture","agent_id":"worker","agent_type":"implementer"}`)
	var verdict strings.Builder
	stop.Stdout = &verdict
	var diagnostics strings.Builder
	stop.Stderr = &diagnostics
	require.NoError(t, stop.Run(), "%s", diagnostics.String())
	require.Equal(t, before, status(), "subagent-stop changed checkout")
	require.Empty(t, verdict.String())
	require.Regexp(t, `stage=handoff check=live outcome=completed .*developer_fingerprint=[0-9a-f]{64}`, diagnosticLog(t, home))

	revoke := exec.CommandContext(ctx, ethosBinary, "handoff", "revoke-live", "project")
	revoke.Dir, revoke.Env = root, env
	output, err := revoke.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Equal(t, before, status(), "revocation changed checkout")
}
