//go:build linux || darwin

package test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestNightlyBiffWallOrdering(t *testing.T) {
	const wallURL = "https://github.com/punt-labs/ethos/actions/runs/"
	const failure = "Nightly QA failure on Ethos — "
	const cancelled = "Nightly QA cancelled on Ethos — "
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	for _, tc := range []struct {
		name, workflow, run, attempt, drill, previous, want string
		post, reject                                        bool
	}{
		{"older failure", "behavioral", "9", "1", "false", cancelled + wallURL + "10/attempts/1", cancelled + wallURL + "10/attempts/1", false, false},
		{"older cancellation", "nightly-cancellation", "9", "3", "false", failure + wallURL + "10/attempts/1", failure + wallURL + "10/attempts/1", false, false},
		{"older attempt", "behavioral", "10", "2", "false", failure + wallURL + "10/attempts/10", failure + wallURL + "10/attempts/10", false, false},
		{"newer cancellation", "nightly-cancellation", "10", "2", "false", failure + wallURL + "9/attempts/9", cancelled + wallURL + "10/attempts/2", true, false},
		{"newer attempt", "behavioral", "10", "2", "false", failure + wallURL + "10/attempts/1", failure + wallURL + "10/attempts/2", true, false},
		{"drill label", "behavioral", "10", "2", "true", "", "DRILL: " + failure + wallURL + "10/attempts/2", true, false},
		{"older drill", "behavioral", "9", "1", "true", "DRILL: " + failure + wallURL + "10/attempts/1", "DRILL: " + failure + wallURL + "10/attempts/1", false, false},
		{"drill protects live wall", "behavioral", "10", "2", "true", failure + wallURL + "9/attempts/1", failure + wallURL + "9/attempts/1", false, true},
		{"other wall message", "behavioral", "10", "2", "false", "Test failed — " + wallURL + "99/attempts/1", failure + wallURL + "10/attempts/2", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := nightlyNotifyRun(t, root, tc.workflow)
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, ".punt-labs/biff"), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(root, "scripts"), filepath.Join(dir, "scripts")))
			stateFile := filepath.Join(dir, "wall.json")
			state := []byte(`{"status":"inactive","wall":null}`)
			if tc.previous != "" {
				state, err = json.Marshal(map[string]string{"text": tc.previous})
				require.NoError(t, err)
			}
			require.NoError(t, os.WriteFile(stateFile, state, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "uvx"), []byte(`#!/usr/bin/env bash
set -eu
while [[ $1 != wall ]]; do shift; done
shift
if (( $# == 0 )); then
  cat "$WALL_TEST_STATE"
else
  jq -n --arg message "$1" '{text:$message}' > "$WALL_TEST_STATE"
  touch "$WALL_TEST_STATE.posted"
fi
`), 0o700))

			cmd := exec.CommandContext(t.Context(), "bash", "-eu", "-o", "pipefail", "-c", run)
			cmd.Dir = dir
			cmd.Env = []string{
				"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
				"WALL_TEST_STATE=" + stateFile, "BIFF_RELAY_TOKEN=offline-fixture",
				"GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=punt-labs/ethos",
				"GITHUB_RUN_ID=" + tc.run, "GITHUB_RUN_ATTEMPT=" + tc.attempt,
				"NIGHTLY_RUN_ID=" + tc.run, "NIGHTLY_RUN_ATTEMPT=" + tc.attempt,
				"BEHAVIORAL_RESULT=failure", "ALERT_DRILL=" + tc.drill,
			}
			out, err := cmd.CombinedOutput()
			if tc.reject {
				require.Error(t, err, "%s", out)
			} else {
				require.NoError(t, err, "%s", out)
			}
			data, err := os.ReadFile(stateFile)
			require.NoError(t, err)
			var wall struct{ Text string }
			require.NoError(t, json.Unmarshal(data, &wall))
			require.Equal(t, tc.want, wall.Text)
			if tc.post {
				require.FileExists(t, stateFile+".posted")
			} else {
				require.NoFileExists(t, stateFile+".posted")
			}
		})
	}
}

func nightlyNotifyRun(t *testing.T, root, workflow string) string {
	t.Helper()
	var file struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string }
		}
	}
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", workflow+".yml"))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &file))
	for _, step := range file.Jobs["notify"].Steps {
		if step.Name == "Notify the Ethos team" {
			require.NotEmpty(t, step.Run)
			return step.Run
		}
	}
	t.Fatal("no Nightly notify step found")
	return ""
}
