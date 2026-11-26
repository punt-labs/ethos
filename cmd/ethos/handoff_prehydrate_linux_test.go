package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubagentStopPrehydratesWorktree(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	runGit(t, repo, "init", "-q")
	dir := filepath.Join(repo, ".punt-labs", "ethos", "checks")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "snapshots"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), []byte("checks: project\n"), 0o600))
	writeGate := func(command string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(`name: project
checks:
  - {name: prehydrate-probe, stage: handoff, files: all, run: [sh, -ec, '`+command+`']}
`), 0o600))
	}
	writeGate(`test "$(cat /scratch/status)" = ready`)
	profile := filepath.Join(dir, "snapshots", "project.yaml")
	writeProfile := func(argv string) {
		t.Helper()
		require.NoError(t, os.WriteFile(profile, []byte(`checks:
  prehydrate-probe:
    profile: linux-worktree-v1
    epoch: prehydrate-v1
    sources: {mode: worktree}
    prehydrate: {run: `+argv+`}
`), 0o600))
	}
	writeProfile("[cp, '{root}/value', '{scratch}/status']")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "value"), []byte("ready\n"), 0o600))
	runGit(t, repo, "add", "--", ".punt-labs/ethos.yaml", ".punt-labs/ethos/checks", "value")
	runGit(t, repo, "commit", "-qm", "prehydrate fixture")
	run := func() string {
		t.Helper()
		cmd := loggingProcess(t, home, repo, "hook", "subagent-stop")
		cmd.Stdin = strings.NewReader(`{"session_id":"fixture","agent_id":"worker","agent_type":"implementer"}`)
		out, err := cmd.Output()
		require.NoError(t, err)
		return string(out)
	}
	out := run()
	if strings.Contains(out, "isolation unavailable on this host") ||
		strings.Contains(out, "make mount propagation private: permission denied") ||
		strings.Contains(out, "make mount propagation private: operation not permitted") {
		t.Skipf("sandbox unavailable: %s", out)
	}
	require.NotContains(t, out, "systemMessage")
	require.NotContains(t, out, `"decision":"block"`)
	require.Regexp(t, `Check finished .*check=prehydrate-probe outcome=completed`, diagnosticLog(t, home))

	writeProfile("[sh, -ec, 'echo hydrate-failure >&2; exit 19']")
	out = run()
	require.Contains(t, out, `"decision":"block"`)
	require.Contains(t, out, "prehydrate-failed")
	require.Contains(t, out, "hydrate-failure")

	writeGate("echo gate-failure; exit 23")
	writeProfile("[sh, -ec, 'i=0; while [ \"$i\" -lt 500 ]; do echo hydrate-noise; echo hydrate-noise >&2; i=$((i+1)); done']")
	out = run()
	require.Contains(t, out, `"decision":"block"`)
	require.Contains(t, out, "gate-failure")
	require.NotContains(t, out, "hydrate-noise")
}
