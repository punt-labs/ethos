//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func postEditShellFixture(t *testing.T) (bin, scratch, processes string) {
	t.Helper()
	home, bin, scratch := t.TempDir(), t.TempDir(), t.TempDir()
	processes = filepath.Join(home, "processes")
	for _, name := range []string{"mktemp", "rm", "cat", "sleep"} {
		path, err := exec.LookPath(name)
		require.NoError(t, err)
		if name == "cat" || name == "sleep" {
			pids := `"$$"`
			if name == "sleep" {
				pids += ` "$PPID"`
			}
			postEditTool(t, bin, name, "printf '%s\\n' "+pids+" >> \"$PROCESS_LOG\"\nexec \""+path+"\" \"$@\"\n")
		} else {
			require.NoError(t, os.Symlink(path, filepath.Join(bin, name)))
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("TMPDIR", scratch)
	t.Setenv("PROCESS_LOG", processes)
	return bin, scratch, processes
}

func postEditShellClean(t *testing.T, scratch, processes string) {
	t.Helper()
	entries, err := os.ReadDir(scratch)
	require.NoError(t, err)
	require.Empty(t, entries, "the hook must remove its temporary input file")
	data, err := os.ReadFile(processes)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		require.NoError(t, err)
		require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "hook child %d is still alive", pid)
	}
}

func TestGeneratedPostEditShellPipe(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude/agents/bwk.md"))
	require.NotContains(t, command, "bash")
	for _, tc := range []struct {
		name, payload, exit string
		killed, noTemp      bool
	}{
		{"payload", `{"tool_input":{"file_path":"a b.go"}}`, "0", false, false},
		{"empty", "", "0", false, false},
		{"malformed", "{invalid", "0", false, false},
		{"failure", "{}", "42", false, false},
		{"killed", "{}", "0", true, false},
		{"temp failure", "{}", "0", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, scratch, processes := postEditShellFixture(t)
			log := filepath.Join(os.Getenv("HOME"), "stdin")
			t.Setenv("STDIN_LOG", log)
			postEditTool(t, bin, "ethos", `test "$1 $2" = 'hook post-edit' || exit 9
test -p /dev/stdin || exit 9
cat > "$STDIN_LOG"
exit `+tc.exit+"\n")
			if tc.killed {
				writeFile(t, filepath.Join(os.Getenv("HOME"), ".punt-hooks-kill"), "")
			}
			if tc.noTemp {
				require.NoError(t, os.Remove(filepath.Join(bin, "mktemp")))
				postEditTool(t, bin, "mktemp", "exit 1\n")
			}
			input := filepath.Join(os.Getenv("HOME"), "input")
			writeFile(t, input, tc.payload)
			stdin, err := os.Open(input)
			require.NoError(t, err)
			defer stdin.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
			cmd.Stdin = stdin
			cmd.WaitDelay = 200 * time.Millisecond
			start := time.Now()
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			require.Less(t, time.Since(start), time.Second, "closed stdin must not leave a watchdog holding output open")
			require.Empty(t, output)
			if tc.killed || tc.noTemp {
				require.NoFileExists(t, log)
			} else {
				data, err := os.ReadFile(log)
				require.NoError(t, err)
				require.Equal(t, tc.payload, string(data))
			}
			postEditShellClean(t, scratch, processes)
		})
	}
}

func TestGeneratedPostEditShellOpenInput(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude/agents/bwk.md"))
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	shells := map[string]string{"sh": "/bin/sh", "bash": bash, "no-bash": "/bin/sh"}
	if bash32 := os.Getenv("ETHOS_TEST_BASH32"); bash32 != "" {
		shells["bash32"] = bash32
	}
	for name, shell := range shells {
		t.Run(name, func(t *testing.T) {
			for _, open := range []bool{false, true} {
				label := "closed"
				if open {
					label = "open"
				}
				t.Run(label, func(t *testing.T) {
					bin, scratch, processes := postEditShellFixture(t)
					require.NoError(t, os.Symlink(shell, filepath.Join(bin, "sh")))
					if name != "no-bash" {
						require.NoError(t, os.Symlink(shell, filepath.Join(bin, "bash")))
					}
					postEditTool(t, bin, "ethos", `test "$1 $2" = 'hook post-edit' || exit 9
test -p /dev/stdin || exit 9
cat
`)
					reader, writer, err := os.Pipe()
					require.NoError(t, err)
					defer reader.Close()
					defer writer.Close()
					payload := `{"tool_input":{"file_path":"file.go","new_string":"` + strings.Repeat("x", 128*1024) + `"}}` + "\n"
					done := make(chan error, 1)
					go func() {
						for start := 0; start < len(payload); start += 4096 {
							_, err := writer.WriteString(payload[start:min(start+4096, len(payload))])
							if err != nil {
								done <- err
								return
							}
						}
						if !open {
							done <- writer.Close()
						} else {
							done <- nil
						}
					}()
					bound := time.Second
					if open {
						bound = 13 * time.Second
					}
					ctx, cancel := context.WithTimeout(context.Background(), bound)
					defer cancel()
					cmd := exec.CommandContext(ctx, filepath.Join(bin, "sh"), "-c", command)
					cmd.Stdin = reader
					cmd.WaitDelay = 200 * time.Millisecond
					start := time.Now()
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s", out)
					require.Less(t, time.Since(start), bound)
					require.Equal(t, payload, string(out))
					require.NoError(t, <-done)
					postEditShellClean(t, scratch, processes)
				})
			}
		})
	}
}

func TestGeneratedPostEditShellStalledInput(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude/agents/bwk.md"))
	for _, payload := range []string{"", `{"tool_input":`} {
		t.Run(payload, func(t *testing.T) {
			bin, scratch, processes := postEditShellFixture(t)
			postEditTool(t, bin, "ethos", `test -p /dev/stdin || exit 9
cat
`)
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			defer reader.Close()
			defer writer.Close()
			_, err = writer.WriteString(payload)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 13*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
			cmd.Stdin = reader
			cmd.WaitDelay = 200 * time.Millisecond
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Equal(t, payload, string(out))
			postEditShellClean(t, scratch, processes)
		})
	}
}

func TestGeneratedPostEditShellInterrupted(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude/agents/bwk.md"))
	bin, scratch, processes := postEditShellFixture(t)
	postEditTool(t, bin, "ethos", "exit 9\n")
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Stdin = reader
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(processes)
		return err == nil && len(strings.Fields(string(data))) >= 3
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(t, cmd.Wait(), "%s", out.String())
	postEditShellClean(t, scratch, processes)
}
