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

	"github.com/stretchr/testify/require"
)

func TestGeneratedGateStorageBoundary(t *testing.T) {
	require.NotEmpty(t, ethosBinary)
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude", "agents", "bwk.md"))
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, path string
		checks     bool
	}{
		{"record source", ".punt-labs/ethos/scripts/worker.sh", true},
		{"record YAML", ".punt-labs/ethos/roles/worker.yaml", false},
		{"repo configuration", ".punt-labs/ethos.yaml", false},
		{"source shell", "scripts/worker.sh", true},
		{"local scratch", ".punt-labs/local/ethos/worker.sh", false},
		{"other local data", ".punt-labs/local/worker.sh", false},
		{"development scratch", ".tmp/worker.sh", false},
		{"similar source prefix", ".punt-labs/local-source/worker.sh", true},
		{"documentation", "docs/guide.md", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, bin := postEditFixture(t)
			require.NoError(t, os.Symlink(cat, filepath.Join(bin, "cat")))
			file := filepath.Join(project, tc.path)
			writeFile(t, file, "")
			marker := filepath.Join(project, "check-ran")
			t.Setenv("POST_EDIT_MARKER", marker)
			postEditTool(t, bin, "shellcheck", "printf ran > \"$POST_EDIT_MARKER\"\necho CHECK_DIAGNOSTIC; exit 7\n")
			postEditTool(t, bin, "ethos", "exec \""+ethosBinary+"\" \"$@\"\n")
			payload, err := json.Marshal(map[string]any{"tool_input": map[string]any{"file_path": file}})
			require.NoError(t, err)
			cmd := exec.Command("/bin/sh", "-c", command)
			cmd.Stdin = strings.NewReader(string(payload))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			require.NoError(t, cmd.Run(), "an edit check must never block")
			require.Empty(t, stderr.String())
			_, err = os.Stat(marker)
			if tc.checks {
				require.NoError(t, err)
				require.Contains(t, stdout.String(), "CHECK_DIAGNOSTIC")
				require.Contains(t, stdout.String(), "\"hookEventName\":\"PostToolUse\"")
			} else {
				require.ErrorIs(t, err, os.ErrNotExist)
				require.Empty(t, stdout.String())
			}
		})
	}
}
