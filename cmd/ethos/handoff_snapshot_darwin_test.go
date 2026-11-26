package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/require"
)

func TestHandoffSnapshotLaunchVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name, run, script, verdict, outcome string
		launches                            int
	}{
		{"clean", "[/bin/sh, -c, 'exit 0']", "", "", "completed", 1},
		{"broken", "[/bin/sh, -c, 'exit 127']", "", "exit status 127", "failed", 1},
		{"missing-executable", "['./gate-script']", "", "sandbox launch did not complete", "deferred", 0},
		{"not-executable", "['./gate-script']", "exit 0\n", "sandbox launch did not complete", "deferred", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			runGit(t, root, "init", "-q")
			for name, body := range map[string]string{
				".gitignore":                                     ".punt-labs/ethos/sessions/\n.punt-labs/local/\n",
				".punt-labs/ethos.yaml":                          "checks: project\n",
				".punt-labs/ethos/checks/project.yaml":           "name: project\nchecks:\n  - {name: snapshot, stage: handoff, files: all, run: " + tc.run + "}\n",
				".punt-labs/ethos/checks/snapshots/project.yaml": "checks:\n  snapshot:\n    profile: linux-worktree-v1\n    epoch: proof\n    sources: {mode: worktree}\n",
			} {
				path := filepath.Join(root, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
			}
			if tc.script != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, "gate-script"), []byte(tc.script), 0o600))
			}
			runGit(t, root, "add", ".")
			runGit(t, root, "commit", "-qm", "snapshot fixture")
			status := func() {
				t.Helper()
				out, err := testhelpers.GitCommand("-C", root, "--no-optional-locks", "status", "--porcelain").CombinedOutput()
				require.NoError(t, err, "%s", out)
				require.Empty(t, out)
			}
			status()
			hook := exec.CommandContext(t.Context(), ethosBinary, "hook", "subagent-stop")
			hook.Dir = root
			hook.Env = testhelpers.CoverEnv([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")})
			hook.Stdin = strings.NewReader(`{"session_id":"fixture","agent_id":"worker","agent_type":"implementer"}`)
			var stderr strings.Builder
			hook.Stderr = &stderr
			start := time.Now()
			output, err := hook.Output()
			elapsed := time.Since(start)
			require.NoError(t, err, "%s", stderr.String())
			status()
			if tc.verdict == "" {
				require.Empty(t, output)
			} else {
				require.Contains(t, string(output), tc.verdict)
			}
			log := diagnosticLog(t, home)
			require.Contains(t, log, "stage=handoff check=snapshot outcome="+tc.outcome)
			require.Equal(t, tc.launches, strings.Count(log, "stage=handoff check=snapshot outcome=launched"))
			t.Logf("snapshot case=%s elapsed=%s outcome=%s launches=%d checkout=clean verdict=%q",
				tc.name, elapsed, tc.outcome, tc.launches, strings.TrimSpace(string(output)))
		})
	}
}
