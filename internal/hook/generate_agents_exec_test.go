//go:build linux || darwin

package hook

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPostToolUseHook_Execution(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "TestMain must build the hook binary")
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude", "agents", "bwk.md"))
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "silent pass", true: "advisory failure"}[fails], func(t *testing.T) {
			project, bin := postEditFixture(t)
			writeFile(t, filepath.Join(project, "file.sh"), "")
			body := "exit 0\n"
			if fails {
				body = "echo SCOPED_FAILURE >&2; exit 1\n"
			}
			postEditTool(t, bin, "shellcheck", body)
			postEditTool(t, bin, "ethos", "exec \""+ethosBinary+"\" \"$@\"\n")
			cmd := exec.Command("/bin/sh", "-c", command)
			cmd.Env = os.Environ()
			cmd.Stdin = strings.NewReader(`{"tool_input":{"file_path":"file.sh"}}`)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			require.NoError(t, cmd.Run())
			require.Empty(t, stderr.String())
			if fails {
				require.Contains(t, stdout.String(), "SCOPED_FAILURE")
				require.Contains(t, stdout.String(), "\"hookEventName\":\"PostToolUse\"")
			} else {
				require.Empty(t, stdout.String())
			}
		})
	}
}

// extractPostToolUseCommand parses the agent file's YAML frontmatter
// and returns the PostToolUse hook command with YAML escaping undone.
func extractPostToolUseCommand(t *testing.T, agentPath string) string {
	t.Helper()

	data, err := os.ReadFile(agentPath)
	require.NoError(t, err)

	// Frontmatter is the block between the first two "---" lines.
	content := string(data)
	require.True(t, strings.HasPrefix(content, "---\n"), "agent file must open with frontmatter")
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	require.GreaterOrEqual(t, end, 0, "frontmatter must close with ---")
	frontmatter := rest[:end]

	var fm struct {
		Hooks struct {
			PostToolUse []struct {
				Hooks []struct {
					Command string `yaml:"command"`
				} `yaml:"hooks"`
			} `yaml:"PostToolUse"`
		} `yaml:"hooks"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(frontmatter), &fm))
	require.Len(t, fm.Hooks.PostToolUse, 1)
	require.Len(t, fm.Hooks.PostToolUse[0].Hooks, 1)

	cmd := fm.Hooks.PostToolUse[0].Hooks[0].Command
	require.NotEmpty(t, cmd)
	return cmd
}
