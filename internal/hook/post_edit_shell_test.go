//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGeneratedPostEditShellPipe(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude/agents/bwk.md"))
	require.NotContains(t, command, "/bin/bash")
	require.NotContains(t, command, "bash")
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
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
			require.NoError(t, os.Symlink(cat, filepath.Join(bin, "cat")))
			t.Setenv("PATH", bin)
			log := filepath.Join(home, "stdin")
			t.Setenv("STDIN_LOG", log)
			postEditTool(t, bin, "ethos", "if [ \"$2\" = pipe-input ]; then exec \""+ethosBinary+"\" \"$@\"; fi\ntest -p /dev/stdin || exit 9\ncat > \"$STDIN_LOG\"\nexit "+tc.exit+"\n")
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
	require.NotEmpty(t, ethosBinary)
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude/agents/bwk.md"))
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	shells := map[string]string{"sh": "/bin/sh", "bash": bash}
	if bash32 := os.Getenv("ETHOS_TEST_BASH32"); bash32 != "" {
		shells["bash32"] = bash32
	}
	for name, shell := range shells {
		t.Run(name, func(t *testing.T) {
			for _, size := range []int{0, 128 * 1024} {
				t.Run(fmt.Sprint(size), func(t *testing.T) {
					bin := t.TempDir()
					t.Setenv("HOME", t.TempDir())
					require.NoError(t, os.Symlink(shell, filepath.Join(bin, "sh")))
					require.NoError(t, os.Symlink(shell, filepath.Join(bin, "bash")))
					require.NoError(t, os.Symlink(cat, filepath.Join(bin, "cat")))
					t.Setenv("PATH", bin)
					postEditTool(t, bin, "ethos", "if [ \"$2\" = pipe-input ]; then exec \""+ethosBinary+"\" \"$@\"; fi\ntest -p /dev/stdin || exit 9\ncat\n")
					reader, writer, err := os.Pipe()
					require.NoError(t, err)
					defer reader.Close()
					defer writer.Close()
					payload := `{"tool_input":{"file_path":"file.go","new_string":"` + strings.Repeat("x", size) + `"}}` + "\n"
					done := make(chan error, 1)
					go func() {
						for start := 0; start < len(payload); start += 4096 {
							_, err := writer.WriteString(payload[start:min(start+4096, len(payload))])
							if err != nil {
								done <- err
								return
							}
						}
						done <- nil
					}()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, "sh", "-c", command)
					cmd.Stdin = reader
					cmd.WaitDelay = time.Second
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s", out)
					require.JSONEq(t, payload, string(out))
					require.NoError(t, <-done)
				})
			}
		})
	}
}

func TestGeneratedPostEditShellStalledInput(t *testing.T) {
	require.NotEmpty(t, ethosBinary)
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude/agents/bwk.md"))
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	for _, payload := range []string{"", `{"tool_input":`} {
		t.Run(payload, func(t *testing.T) {
			bin := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			require.NoError(t, os.Symlink(cat, filepath.Join(bin, "cat")))
			t.Setenv("PATH", bin)
			postEditTool(t, bin, "ethos", "if [ \"$2\" = pipe-input ]; then exec \""+ethosBinary+"\" \"$@\"; fi\ntest -p /dev/stdin || exit 9\ncat\n")
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			defer reader.Close()
			defer writer.Close()
			_, err = writer.WriteString(payload)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
			cmd.Stdin = reader
			cmd.WaitDelay = time.Second
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			require.NoError(t, cmd.Run(), "%s", stderr.String())
			require.Equal(t, payload, stdout.String())
			require.Contains(t, stderr.String(), "hook input timed out")
		})
	}
}
