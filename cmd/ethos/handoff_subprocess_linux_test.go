package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubagentStopWorktreeBashProcessSubstitution(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	runGit(t, repo, "init", "-q")
	dir := filepath.Join(repo, ".punt-labs", "ethos", "checks")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "snapshots"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), []byte("checks: project\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(`name: project
checks:
  - {name: process-substitution, stage: handoff, files: all, run: [bash, -e, gate.sh]}
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "snapshots", "project.yaml"), []byte(`checks:
  process-substitution:
    profile: linux-worktree-v1
    epoch: process-substitution-v1
    sources: {mode: worktree}
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "gate.sh"), []byte(`test "$(cat <(echo ok))" = ok`+"\n"), 0o600))
	runGit(t, repo, "add", "--", ".punt-labs/ethos.yaml", ".punt-labs/ethos/checks", "gate.sh")
	runGit(t, repo, "commit", "-qm", "process substitution fixture")

	cmd := loggingProcess(t, home, repo, "hook", "subagent-stop")
	cmd.Stdin = strings.NewReader(`{"session_id":"fixture","agent_id":"worker","agent_type":"implementer"}`)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	if strings.Contains(string(out), "isolation unavailable on this host") ||
		strings.Contains(string(out), "make mount propagation private: permission denied") ||
		strings.Contains(string(out), "make mount propagation private: operation not permitted") {
		t.Skipf("sandbox unavailable: %s", out)
	}
	require.NotContains(t, string(out), "systemMessage")
	require.NotContains(t, string(out), `"decision":"block"`)
	require.Regexp(t, `Check finished .*check=process-substitution outcome=completed`, diagnosticLog(t, home))

	t.Run("build identity", func(t *testing.T) {
		run := func(binary string) {
			t.Helper()
			cmd := loggingProcess(t, home, repo, "hook", "subagent-stop")
			cmd.Path = binary
			cmd.Args[0] = binary
			cmd.Stdin = strings.NewReader(`{"session_id":"fixture","agent_id":"worker","agent_type":"implementer"}`)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.NotContains(t, string(out), "systemMessage")
		}
		run(ethosBinary)
		const reused = "check=process-substitution outcome=reused"
		const missed = "check=process-substitution outcome=miss"
		before := diagnosticLog(t, home)
		require.Equal(t, 1, strings.Count(before, reused))
		require.Equal(t, 1, strings.Count(before, missed))

		// Linux ignores trailing bytes when loading ELF. Keep the runner's
		// behavior identical while giving it a distinct executable digest.
		candidate := filepath.Join(t.TempDir(), "ethos")
		executable, err := os.ReadFile(ethosBinary)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(candidate, append(executable, '2'), 0o700))
		run(candidate)
		after := diagnosticLog(t, home)
		require.Equal(t, 1, strings.Count(after, reused))
		require.Equal(t, 2, strings.Count(after, missed))
		run(candidate)
		require.Equal(t, 2, strings.Count(diagnosticLog(t, home), reused))
	})
}
