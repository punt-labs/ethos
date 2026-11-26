package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubagentStopRefreshDataRecovery(t *testing.T) {
	for _, kind := range []string{"normal", "linked", "bare-sibling"} {
		t.Run(kind, func(t *testing.T) {
			base, home := t.TempDir(), t.TempDir()
			primary := filepath.Join(base, "primary")
			runGit(t, base, "init", "-q", primary)
			runGit(t, primary, "commit", "-qm", "fixture", "--allow-empty")
			checkout, store := primary, primary
			switch kind {
			case "linked":
				checkout = filepath.Join(base, "worker@example.test")
				runGit(t, primary, "worktree", "add", "--detach", checkout)
			case "bare-sibling":
				bare := filepath.Join(base, "bare.git")
				runGit(t, base, "clone", "--bare", primary, bare)
				runGit(t, bare, "worktree", "add", "--detach", filepath.Join(base, "sibling"))
				checkout = filepath.Join(base, "bare-worker@example.test")
				runGit(t, bare, "worktree", "add", "--detach", checkout)
				store = checkout
			}
			for path, contents := range map[string]string{
				".punt-labs/ethos.yaml": "checks: project\n",
				".punt-labs/ethos/checks/project.yaml": "name: project\nchecks:\n" +
					"  - {name: recovery, stage: handoff, files: all, run: [sh, gate.sh]}\n",
				".punt-labs/ethos/checks/snapshots/project.yaml": fmt.Sprintf(`checks:
  recovery:
    profile: linux-worktree-v1
    epoch: recovery-v1
    sources: {mode: worktree}
    data: [{name: fixture, url: 'https://%s/%s', max_age_hours: 1, mount: value}]
`, handoffFixtureIP, kind),
				"gate.sh": "set -eu\n" +
					"test \"$(cat /runtime/data/value)\" = " + kind + "\n" +
					"if (printf changed >> /runtime/data/value) 2>/dev/null; then exit 92; fi\n",
			} {
				full := filepath.Join(checkout, path)
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
				require.NoError(t, os.WriteFile(full, []byte(contents), 0o600))
			}
			runGit(t, checkout, "add", "--", ".punt-labs", "gate.sh")
			runGit(t, checkout, "commit", "-qm", "recovery policy")

			audit := loggingProcess(t, home, checkout, "hook", "audit-log")
			audit.Stdin = strings.NewReader(fmt.Sprintf(`{"session_id":"recovery","agent_id":"worker","agent_type":"implementer","tool_name":"Write","tool_input":{"file_path":%q}}`, filepath.Join(checkout, "gate.sh")))
			out, err := audit.CombinedOutput()
			require.NoError(t, err, "%s", out)
			row, err := os.ReadFile(filepath.Join(store, ".punt-labs/local/ethos/sessions/recovery.audit.jsonl"))
			require.NoError(t, err)
			require.Contains(t, string(row), `"agent_id":"worker"`)
			require.Contains(t, string(row), `"tool":"Write"`)

			stop := func() string {
				t.Helper()
				cmd := loggingProcess(t, home, checkout, "hook", "subagent-stop")
				cmd.Stdin = strings.NewReader(`{"session_id":"recovery","agent_id":"worker","agent_type":"implementer"}`)
				out, err := cmd.Output()
				require.NoError(t, err)
				return string(out)
			}
			first := stop()
			if strings.Contains(first, "isolation unavailable on this host") {
				t.Skipf("sandbox unavailable: %s", first)
			}
			var outcome struct {
				Decision string `json:"decision"`
				Notice   string `json:"systemMessage"`
			}
			require.NoError(t, json.Unmarshal([]byte(first), &outcome), "%s", first)
			require.Equal(t, "block", outcome.Decision)
			_, command, found := strings.Cut(outcome.Notice, `data-unavailable: "fixture" is missing, expired or invalid; run `)
			require.True(t, found, outcome.Notice)
			command, suffix, found := strings.Cut(command, " in the checkout this stop checked.")
			require.True(t, found, outcome.Notice)
			require.Equal(t, "ethos handoff refresh-data --agent implementer", command)
			require.Contains(t, suffix, "Run the explicit project gate:")
			log := diagnosticLog(t, home)
			require.Contains(t, log, "check=recovery outcome=deferred")
			require.NotContains(t, log, "check=recovery outcome=launched")

			env, _, tunnels := handoffTLSFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/"+kind {
					http.NotFound(w, req)
					return
				}
				_, _ = io.WriteString(w, kind)
			}))
			refresh := loggingProcess(t, home, checkout, strings.Fields(command)[1:]...)
			refresh.Env = append(refresh.Env, env...)
			out, err = refresh.Output()
			require.NoError(t, err, "%s", out)
			require.Equal(t, "fixture\n", string(out))
			require.EqualValues(t, 1, tunnels.Load())

			second := stop()
			if strings.Contains(second, "make mount propagation private: permission denied") ||
				strings.Contains(second, "make mount propagation private: operation not permitted") {
				t.Skipf("sandbox unavailable: %s", second)
			}
			require.Empty(t, second)
			log = diagnosticLog(t, home)[len(log):]
			require.Contains(t, log, "check=recovery outcome=miss")
			require.Contains(t, log, "check=recovery outcome=launched")
			require.Contains(t, log, "check=recovery outcome=completed")
			t.Log("real stop deferred, printed command refreshed its checkout, next stop launched and read mounted data")
		})
	}
}
