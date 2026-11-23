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

// postEditCheckPIDs reads the PIDs the fake check and its child recorded.
func postEditCheckPIDs(path string) []int {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			return nil
		}
		pids = append(pids, pid)
	}
	return pids
}

// TestGeneratedPostEditShellCancelStopsChecks proves that cancelling the
// generated PostToolUse hook stops the edit checks `ethos hook post-edit`
// started. A harness may signal the hook shell alone (Node's child.kill),
// its process group, or kill the shell outright; each must stop the check,
// which runs in a process group of its own, and the check's own children.
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
				postEditTool(t, bin, "shellcheck", "sleep 30 &\nprintf '%s %s\\n' \"$$\" \"$!\" > \"$CHECK_PID.tmp\"\nmv \"$CHECK_PID.tmp\" \"$CHECK_PID\"\nwait\n")
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
				var pids []int
				defer func() {
					for _, pid := range pids {
						_ = syscall.Kill(-pid, syscall.SIGKILL)
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}()
				require.Eventually(t, func() bool {
					pids = postEditCheckPIDs(checkPID)
					return len(pids) == 2 && postEditProcessAlive(pids[0]) && postEditProcessAlive(pids[1])
				}, 10*time.Second, 10*time.Millisecond, "the edit check never started: %s", &out)

				target := cmd.Process.Pid
				if mode.group {
					target = -target
				}
				cancelled := time.Now()
				require.NoError(t, syscall.Kill(target, mode.signal))
				require.Eventually(t, func() bool { return !postEditProcessAlive(pids[0]) && !postEditProcessAlive(pids[1]) }, 3*time.Second, 10*time.Millisecond,
					"edit check %v or its child still running after the hook was cancelled", pids)
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

// TestGeneratedPostEditShellSparesOldPostEdit proves that cancelling the hook
// does not TERM a post-edit that cannot stop its checks (an Ethos without
// `stdin-watchdog --probe --cancel`): that post-edit would die and orphan its
// checks without their deadline, so it is left to finish on its own.
func TestGeneratedPostEditShellSparesOldPostEdit(t *testing.T) {
	require.NotEmpty(t, ethosBinary)
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	mv, err := exec.LookPath("mv")
	require.NoError(t, err)
	bin, scratch, _ := postEditShellFixture(t)
	require.NoError(t, os.Symlink(sleep, filepath.Join(bin, "sleep")))
	require.NoError(t, os.Symlink(mv, filepath.Join(bin, "mv")))
	termLog := filepath.Join(os.Getenv("HOME"), "term")
	pidLog := filepath.Join(os.Getenv("HOME"), "post-edit.pid")
	t.Setenv("TERM_LOG", termLog)
	t.Setenv("PE_PID", pidLog)
	postEditTool(t, bin, "ethos", "case \"$*\" in\n*--cancel*) exit 2 ;;\n'hook stdin-watchdog'*) exec \""+ethosBinary+"\" \"$@\" ;;\n'hook post-edit'*) trap 'echo term > \"$TERM_LOG\"; exit 0' TERM; echo $$ > \"$PE_PID.tmp\"; mv \"$PE_PID.tmp\" \"$PE_PID\"; while :; do sleep 0.05; done ;;\nesac\nexit 9\n")
	cmd := exec.Command("/bin/sh", "-c", postEditShellCommand("bwk"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(`{"tool_input":{"file_path":"x.sh"}}`)
	// A file, not a pipe: the spared post-edit keeps its output open.
	out, err := os.Create(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)
	defer out.Close()
	cmd.Stdout, cmd.Stderr = out, out
	require.NoError(t, cmd.Start())
	var pid int
	defer func() {
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}()
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidLog)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "post-edit never started")
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled hook must exit promptly")
	}
	time.Sleep(200 * time.Millisecond)
	_, err = os.Stat(termLog)
	require.True(t, os.IsNotExist(err), "an old post-edit must not be sent TERM")
	require.True(t, postEditProcessAlive(pid), "an old post-edit must be left to finish")
	entries, err := os.ReadDir(scratch)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestPostEditStopsChecksWhenHookShellAlreadyGone proves post-edit cancels its
// checks when the shell named by ETHOS_HOOK_SHELL died before post-edit could
// see its parent change.
func TestPostEditStopsChecksWhenHookShellAlreadyGone(t *testing.T) {
	require.NotEmpty(t, ethosBinary)
	gone := exec.Command("true")
	require.NoError(t, gone.Run())
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	root, bin := postEditFixture(t)
	require.NoError(t, os.Symlink(sleep, filepath.Join(bin, "sleep")))
	checkPID := filepath.Join(t.TempDir(), "check.pid")
	t.Setenv("CHECK_PID", checkPID)
	postEditTool(t, bin, "shellcheck", "echo $$ > \"$CHECK_PID\"\nexec sleep 30\n")
	edited := filepath.Join(root, "edited.sh")
	writeFile(t, edited, "echo hi\n")
	cmd := exec.Command(ethosBinary, "hook", "post-edit", "--agent", "bwk")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ETHOS_HOOK_SHELL="+strconv.Itoa(gone.Process.Pid), "CLAUDE_PROJECT_DIR="+root)
	cmd.Stdin = strings.NewReader(`{"tool_input":{"file_path":"` + edited + `"}}`)
	started := time.Now()
	out, _ := cmd.CombinedOutput()
	require.Less(t, time.Since(started), 3*time.Second, "%s", out)
	require.NotContains(t, string(out), "hookSpecificOutput", "a cancelled post-edit writes no feedback")
	if data, err := os.ReadFile(checkPID); err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		require.NoError(t, err)
		require.Eventually(t, func() bool { return !postEditProcessAlive(pid) }, time.Second, 10*time.Millisecond)
	}
}
