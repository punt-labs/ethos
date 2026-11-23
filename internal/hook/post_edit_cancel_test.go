//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"errors"
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

// postEditProcessAlive reports whether pid is a live, unreaped-or-running
// process. A zombie whose parent died counts as stopped.
var postEditPS = func() string {
	path, err := exec.LookPath("ps")
	if err != nil {
		return "ps"
	}
	return path
}()

func postEditProcessAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	out, err := exec.Command(postEditPS, "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(out))
	return state != "" && !strings.HasPrefix(state, "Z")
}

// TestGeneratedPostEditShellCancelStopsChecks proves that cancelling the
// generated PostToolUse hook stops the edit checks `ethos hook post-edit`
// started. A harness may signal the hook shell alone (Node's child.kill),
// its process group, or kill the shell outright; each must stop the check,
// which runs in a process group of its own.
func TestGeneratedPostEditShellCancelStopsChecks(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the real ethos binary must be built")
	shells := map[string][]string{"sh": {"/bin/sh"}}
	if bash, err := exec.LookPath("bash"); err == nil {
		shells["bash"] = []string{bash}
	}
	if busybox, err := exec.LookPath("busybox"); err == nil {
		shells["busybox-ash"] = []string{busybox, "ash"}
	}
	modes := []struct {
		name   string
		signal syscall.Signal
		group  bool
	}{
		{"shell TERM", syscall.SIGTERM, false},
		{"shell INT", syscall.SIGINT, false},
		{"shell HUP", syscall.SIGHUP, false},
		{"group TERM", syscall.SIGTERM, true},
		{"shell KILL", syscall.SIGKILL, false},
	}
	for name, shell := range shells {
		for _, mode := range modes {
			t.Run(name+"/"+mode.name, func(t *testing.T) {
				tools := map[string]string{}
				for _, tool := range []string{"git", "sleep", "ps", "mv"} {
					path, err := exec.LookPath(tool)
					require.NoError(t, err)
					tools[tool] = path
				}
				bin, scratch, _ := postEditShellFixture(t)
				for tool, path := range tools {
					require.NoError(t, os.Symlink(path, filepath.Join(bin, tool)))
				}
				postEditTool(t, bin, "ethos", "exec \""+ethosBinary+"\" \"$@\"\n")
				checkPID := filepath.Join(os.Getenv("HOME"), "check.pid")
				t.Setenv("CHECK_PID", checkPID)
				postEditTool(t, bin, "shellcheck", "printf '%s\\n' \"$$\" > \"$CHECK_PID.tmp\"\nmv \"$CHECK_PID.tmp\" \"$CHECK_PID\"\nexec sleep 30\n")
				repo := t.TempDir()
				initGitRepo(t, repo)
				edited := filepath.Join(repo, "edited.sh")
				writeFile(t, edited, "echo hi\n")
				t.Setenv("CLAUDE_PROJECT_DIR", repo)

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				args := append(append([]string{}, shell[1:]...), "-c", postEditShellCommand("bwk"))
				cmd := exec.CommandContext(ctx, shell[0], args...)
				cmd.Dir = repo
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
				cmd.WaitDelay = 200 * time.Millisecond
				cmd.Stdin = strings.NewReader(`{"tool_input":{"file_path":"` + edited + `"}}`)
				var out bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &out
				require.NoError(t, cmd.Start())
				var pid int
				defer func() {
					if pid > 0 {
						_ = syscall.Kill(-pid, syscall.SIGKILL)
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}()
				require.Eventually(t, func() bool {
					data, err := os.ReadFile(checkPID)
					if err != nil {
						return false
					}
					pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
					return err == nil && postEditProcessAlive(pid)
				}, 10*time.Second, 10*time.Millisecond, "the edit check never started: %s", &out)

				target := cmd.Process.Pid
				if mode.group {
					target = -target
				}
				cancelled := time.Now()
				require.NoError(t, syscall.Kill(target, mode.signal))
				require.Eventually(t, func() bool { return !postEditProcessAlive(pid) }, 3*time.Second, 10*time.Millisecond,
					"edit check %d still running after the hook was cancelled", pid)
				t.Logf("check stopped %s after cancellation", time.Since(cancelled).Round(time.Millisecond))
				_ = cmd.Wait()
				require.Less(t, time.Since(cancelled), 3*time.Second, "the cancelled hook must exit promptly: %s", &out)
				require.Eventually(t, func() bool {
					entries, err := os.ReadDir(scratch)
					return err == nil && len(entries) == 0
				}, time.Second, 10*time.Millisecond, "the hook must remove its temporary input file")
			})
		}
	}
}
