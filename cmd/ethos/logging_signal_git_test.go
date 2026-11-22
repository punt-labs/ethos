//go:build linux || darwin

package main

import (
	"errors"
	"fmt"
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

func TestSharedLoggingGitTermination(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		signal     syscall.Signal
		ignored    bool
		descendant bool
	}{
		{"cli-interrupt", []string{"identity", "list"}, syscall.SIGINT, false, false},
		{"cli-terminate", []string{"identity", "list"}, syscall.SIGTERM, false, false},
		{"hook-interrupt", []string{"hook", "pre-commit"}, syscall.SIGINT, false, false},
		{"inherited-ignore", []string{"identity", "list"}, syscall.SIGTERM, true, false},
		{"cleanup-in-flight", []string{"identity", "list"}, syscall.SIGINT, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd, bin := t.TempDir(), t.TempDir(), t.TempDir()
			pidFile := filepath.Join(bin, "git.pid")
			sleep, err := exec.LookPath("sleep")
			require.NoError(t, err)
			shim := "#!/bin/sh\n[ \"$1\" = --version ] || exit 91\ntrap '' TERM\nprintf '%s\\n' \"$$\" > \"$ETHOS_GIT_PID\"\nexec \"$ETHOS_GIT_SLEEP\" 60\n"
			if tc.descendant {
				shim = `#!/bin/sh
[ "$1" = --version ] || exit 91
trap 'exit 0' TERM
/bin/sh -c 'trap "" TERM; printf "%s\n" "$$" > "$ETHOS_GIT_CHILD"; exec "$ETHOS_GIT_SLEEP" 60' >/dev/null 2>&1 &
while [ ! -s "$ETHOS_GIT_CHILD" ]; do "$ETHOS_GIT_SLEEP" 0.01; done
printf '%s\n' "$$" > "$ETHOS_GIT_PID"
wait
`
			}
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o700))
			cmd := loggingProcess(t, home, cwd, tc.args...)
			cmd.Env = append(cmd.Env, "PATH="+bin, "ETHOS_GIT_PID="+pidFile, "ETHOS_GIT_SLEEP="+sleep, "ETHOS_GIT_CHILD="+filepath.Join(bin, "child.pid"))
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if tc.ignored {
				cmd.Path = "/bin/sh"
				cmd.Args = append([]string{"sh", "-c", `trap '' INT; exec "$@"`, "sh", ethosBinary}, tc.args...)
			}
			require.NoError(t, cmd.Start())
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			var pid int
			t.Cleanup(func() {
				if pid > 0 {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			})
			require.Eventually(t, func() bool {
				data, err := os.ReadFile(pidFile)
				if err != nil {
					return false
				}
				pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
				return err == nil && pid > 0
			}, 5*time.Second, 10*time.Millisecond, "gitenv's Git probe must be running")
			pgid, err := syscall.Getpgid(pid)
			require.NoError(t, err)
			require.Equal(t, pid, pgid, "git must own a separate process group")
			require.NotEqual(t, cmd.Process.Pid, pgid)
			if tc.ignored {
				require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
				select {
				case err := <-done:
					t.Fatalf("inherited-ignored SIGINT terminated ethos: %v", err)
				case <-time.After(200 * time.Millisecond):
				}
				require.NoError(t, syscall.Kill(pid, 0))
			}
			var lock *os.File
			if tc.descendant {
				lock, err = os.OpenFile(filepath.Join(home, ".punt-labs", "ethos", "logs", "ethos.log.rotate.lock"), os.O_RDWR, 0o600)
				require.NoError(t, err)
				t.Cleanup(func() { _ = lock.Close() })
				require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX))
			}
			require.NoError(t, cmd.Process.Signal(tc.signal))
			if tc.descendant {
				require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 500*time.Millisecond, 10*time.Millisecond, "Git must be reaped while its descendant is cleaning up")
				time.Sleep(50 * time.Millisecond)
				require.NoError(t, syscall.Kill(-pgid, 0), "the descendant must still be in the cleanup grace")
				require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
			}
			select {
			case err := <-done:
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				status := exit.Sys().(syscall.WaitStatus)
				require.True(t, status.Signaled(), "preserve signal exit status: %v", exit)
				require.Equal(t, tc.signal, status.Signal())
			case <-time.After(5 * time.Second):
				t.Fatal("ethos did not finish signal cleanup within its bound")
			}
			log := diagnosticLog(t, home)
			require.Equal(t, 1, strings.Count(log, "Invocation finished"), log)
			require.Contains(t, log, "outcome=failed")
			require.Contains(t, log, fmt.Sprintf("exit_code=%d", 128+int(tc.signal)))
			require.Eventually(t, func() bool {
				return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) && errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
			}, 2*time.Second, 20*time.Millisecond, "Git or its group outlived ethos")
		})
	}
}

func TestSharedLoggingNoGitTermination(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cmd := loggingProcess(t, home, cwd, "hook", "post-edit")
	cmd.Env = append(cmd.Env, "PATH="+t.TempDir())
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill() })
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(home, ".punt-labs", "ethos", "telemetry.db"))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
	err = cmd.Wait()
	require.Less(t, time.Since(started), time.Second, "no Git means no cleanup grace is needed")
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	status := exit.Sys().(syscall.WaitStatus)
	require.True(t, status.Signaled())
	require.Equal(t, syscall.SIGINT, status.Signal())
	log := diagnosticLog(t, home)
	require.Equal(t, 1, strings.Count(log, "Invocation finished"), log)
	require.Contains(t, log, "outcome=failed exit_code=130")
}
