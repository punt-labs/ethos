//go:build linux || darwin

package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Execute the decoded command from a real generated agent, not a parallel
// shell implementation. All commands on PATH are fixture-owned: jq checks
// stdin then supplies its selected path, make records the gate invocation,
// and git maps absolute paths to the isolated project without touching Git.
func TestGeneratedGateStorageBoundary(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude", "agents", "bwk.md"))
	t.Logf("executing generated command with isolated jq/make/git harness:\n%s", command)

	paths := []struct {
		name string
		path string
		gate bool
	}{
		{"record role", ".punt-labs/ethos/roles/worker.yaml", true},
		{"record identity", ".punt-labs/ethos/identities/bwk.yaml", true},
		{"embedded record role", "checkout/.punt-labs/ethos/roles/worker.yml", true},
		{"absolute record identity", "/fixture-checkout/.punt-labs/ethos/identities/bwk.yaml", true},
		{"repo config", ".punt-labs/ethos.yaml", true},
		{"source YAML", "internal/config.yaml", true},
		{"source Go", "internal/worker.go", true},
		{"local scratch", ".punt-labs/local/ethos/missions/scratch.yaml", false},
		{"embedded local scratch", "checkout/.punt-labs/local/ethos/scratch.yml", false},
		{"absolute local scratch", "/fixture-checkout/.punt-labs/local/ethos/scratch.yaml", false},
		{"development scratch", ".tmp/check.yaml", false},
		{"embedded development scratch", "checkout/.tmp/check.yaml", false},
		{"other local source", ".punt-labs/local/config.yaml", true},
		{"similarly named source", ".punt-labs/local/ethos-source/config.yaml", true},
		{"markdown", "docs/guide.md", false},
		{"unknown path fails closed", "", true},
	}
	for _, path := range paths {
		for _, gateExit := range []string{"0", "7"} {
			t.Run(path.name+"/make-exit-"+gateExit, func(t *testing.T) {
				project := t.TempDir()
				bin := filepath.Join(project, "bin")
				require.NoError(t, os.MkdirAll(bin, 0o700))
				scripts := map[string]string{
					"jq":   "#!/bin/sh\nIFS= read -r _input\n[ \"$_input\" = \"$GATE_TEST_PAYLOAD\" ] || exit 65\nprintf '%s\\n' \"$GATE_TEST_PATH\"\n",
					"make": "#!/bin/sh\nprintf '%s|%s\\n' \"$PWD\" \"$*\" >> \"$GATE_TEST_CALLS\"\nprintf 'GATE_FIXTURE_DIAGNOSTIC\\n'\nexit \"$GATE_TEST_EXIT\"\n",
					"git":  "#!/bin/sh\n[ \"$1\" = -C ] && [ \"$3\" = rev-parse ] && [ \"$4\" = --show-toplevel ] || exit 66\nprintf '%s\\n' \"$CLAUDE_PROJECT_DIR\"\n",
				}
				for name, script := range scripts {
					require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700))
				}
				// Only these read-only system utilities are reachable; in
				// particular no fallback can invoke the repository's make.
				for _, name := range []string{"dirname", "tail"} {
					tool, err := exec.LookPath(name)
					require.NoError(t, err)
					require.NoError(t, os.Symlink(tool, filepath.Join(bin, name)))
				}
				payload, err := json.Marshal(struct {
					ToolInput struct {
						FilePath string `json:"file_path"`
					} `json:"tool_input"`
				}{ToolInput: struct {
					FilePath string `json:"file_path"`
				}{FilePath: path.path}})
				require.NoError(t, err)
				calls := filepath.Join(project, "gate.calls")
				cmd := exec.Command("/bin/sh", "-c", command)
				cmd.Dir = project
				cmd.Env = []string{
					"PATH=" + bin,
					"CLAUDE_PROJECT_DIR=" + project,
					"GATE_TEST_PAYLOAD=" + string(payload),
					"GATE_TEST_PATH=" + path.path,
					"GATE_TEST_CALLS=" + calls,
					"GATE_TEST_EXIT=" + gateExit,
				}
				cmd.Stdin = strings.NewReader(string(payload) + "\n")
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				exit := 0
				if err := cmd.Run(); err != nil {
					var exitErr *exec.ExitError
					require.ErrorAs(t, err, &exitErr)
					exit = exitErr.ExitCode()
				}
				if path.gate {
					data, err := os.ReadFile(calls)
					assert.NoError(t, err, "source and record paths must reach the gate")
					assert.Equal(t, project+"|check\n", string(data), "exactly one gate in the isolated project")
				} else {
					_, err := os.Stat(calls)
					assert.ErrorIs(t, err, os.ErrNotExist, "scratch and documentation must not invoke the gate")
				}
				assert.Empty(t, stdout.String())
				if path.gate && gateExit != "0" {
					assert.Equal(t, 2, exit, "gate failure must remain visible to the harness")
					assert.Contains(t, stderr.String(), "GATE_FIXTURE_DIAGNOSTIC")
				} else {
					assert.Equal(t, 0, exit)
					assert.Empty(t, stderr.String())
				}
			})
		}
	}
}
