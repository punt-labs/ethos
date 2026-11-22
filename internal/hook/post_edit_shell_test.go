//go:build linux || darwin

package hook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGeneratedPostEditShellPipe(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude/agents/bwk.md"))
	for _, tc := range []struct {
		name, payload, exit string
		killed              bool
	}{
		{"payload", `{"tool_input":{"file_path":"a b.go"}}`, "0", false},
		{"empty", "", "0", false},
		{"malformed", "{invalid", "0", false},
		{"failure", "{}", "42", false},
		{"killed", "{}", "0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, bin := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			log := filepath.Join(home, "stdin")
			t.Setenv("STDIN_LOG", log)
			postEditTool(t, bin, "ethos", "test -p /dev/stdin || exit 9\ncat > \"$STDIN_LOG\"\nexit "+tc.exit+"\n")
			if tc.killed {
				writeFile(t, filepath.Join(home, ".punt-hooks-kill"), "")
			}
			input := filepath.Join(home, "input")
			writeFile(t, input, tc.payload)
			stdin, err := os.Open(input)
			require.NoError(t, err)
			defer stdin.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
			cmd.Stdin = stdin
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			if tc.killed {
				require.NoFileExists(t, log)
			} else {
				data, err := os.ReadFile(log)
				require.NoError(t, err)
				require.Equal(t, tc.payload, string(data))
			}
		})
	}
}

func TestGeneratedPostEditShellOpenInput(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude/agents/bwk.md"))
	bin := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	postEditTool(t, bin, "ethos", "test -p /dev/stdin || exit 9\ncat\n")
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	defer writer.Close()
	_, err = writer.WriteString("{\"tool_input\":{\"file_path\":\"file.go\"}}\n")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Stdin = reader
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	version, err := exec.Command("/bin/bash", "-c", `printf %s "${BASH_VERSINFO[0]}"`).Output()
	require.NoError(t, err)
	if string(version) == "3" {
		require.Empty(t, out, "Bash 3.2 discards a partial read on timeout")
		return
	}
	require.JSONEq(t, `{"tool_input":{"file_path":"file.go"}}`, string(out))
}
