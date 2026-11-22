//go:build linux || darwin

package hook

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGeneratedPostEditShellPipe(t *testing.T) {
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	node, _ := exec.LookPath("node")
	binary := filepath.Join(t.TempDir(), "ethos")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/ethos")
	data, err := build.CombinedOutput()
	require.NoError(t, err, "%s", data)
	config, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(config, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(config, ".claude/agents/bwk.md"))
	for _, mode := range []string{"pipe", "node inherited pipe", "empty", "malformed", "killed"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "node inherited pipe" && node == "" {
				t.Skip("Node unavailable; direct /bin/sh pipe is covered")
			}
			root, bin := postEditFixture(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			require.NoError(t, os.Symlink(binary, filepath.Join(bin, "ethos")))
			require.NoError(t, os.Symlink(cat, filepath.Join(bin, "cat")))
			file := filepath.Join(root, "file with spaces.txt")
			writeFile(t, file, "edited\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, root, "project", "  - name: pipe-probe\n    stage: edit\n    files: ['**/*.txt']\n    run: [pipe-probe, '{file}']\n")
			log := filepath.Join(t.TempDir(), "log")
			t.Setenv("PIPE_LOG", log)
			postEditTool(t, bin, "pipe-probe", "printf '%s\\000' \"$@\" > \"$PIPE_LOG\"\nprintf 'edit pipe advice\\n'\nexit 1\n")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
			if mode == "node inherited pipe" {
				cmd = exec.CommandContext(ctx, node, "-e", `const {spawn}=require('child_process');const p=spawn('/bin/sh',['-c',process.argv[1]],{stdio:'inherit'});p.on('exit',code=>process.exit(code??1));`, command)
			}
			cmd.Dir = root
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			defer reader.Close()
			defer writer.Close()
			cmd.Stdin = reader
			output := filepath.Join(t.TempDir(), "output")
			out, err := os.Create(output)
			require.NoError(t, err)
			defer out.Close()
			cmd.Stdout, cmd.Stderr = out, out
			if mode == "killed" {
				writeFile(t, filepath.Join(home, ".punt-hooks-kill"), "")
			} else {
				payload, err := json.Marshal(map[string]any{"tool_input": map[string]string{"file_path": file}})
				require.NoError(t, err)
				if mode == "empty" {
					payload = nil
				} else if mode == "malformed" {
					payload = []byte("{invalid")
				}
				_, err = writer.Write(payload)
				require.NoError(t, err)
				require.NoError(t, writer.Close())
			}
			require.NoError(t, cmd.Run())
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			if mode == "pipe" || mode == "node inherited pipe" {
				require.Contains(t, string(data), "edit pipe advice")
				data, err = os.ReadFile(log)
				require.NoError(t, err)
				require.Equal(t, file+"\x00", string(data))
			} else {
				require.NoFileExists(t, log)
				if mode == "killed" {
					require.Empty(t, strings.TrimSpace(string(data)))
				}
			}
		})
	}
}

func TestPostEditShellCommandQuotesHandle(t *testing.T) {
	for _, handle := range []string{"bwk", "a'b", "$(touch unsafe)", "a; exit 2"} {
		t.Run(handle, func(t *testing.T) {
			home, bin := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			log := filepath.Join(t.TempDir(), "args")
			t.Setenv("ARG_LOG", log)
			postEditTool(t, bin, "ethos", "printf '%s\\000' \"$@\" > \"$ARG_LOG\"\n")
			cmd := exec.Command("/bin/sh", "-c", postEditShellCommand(handle))
			cmd.Dir = home
			cmd.Stdin = strings.NewReader("{}")
			data, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", data)
			data, err = os.ReadFile(log)
			require.NoError(t, err)
			require.Equal(t, "hook\x00post-edit\x00--agent\x00"+handle+"\x00", string(data))
			require.NoFileExists(t, filepath.Join(home, "unsafe"))
		})
	}
}
