//go:build linux

package gitenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// openPty returns a pseudo-terminal's master and slave.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	require.NoError(t, unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0))
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	require.NoError(t, err)
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close(); _ = s.Close() })
	return m, s
}

// TestHelperRunNetworkGit is the child of the session tests, not a test: it runs
// one Network git through Run, as ethos would from a terminal.
func TestHelperRunNetworkGit(t *testing.T) {
	git := os.Getenv("ETHOS_HELPER_GIT")
	if git == "" {
		t.Skip("helper process")
	}
	_, _ = Run(Spec{Git: git, Network: true, Limit: 30 * time.Second, Args: []string{"clone", "u"}})
}

// startUnderPty runs the helper as a session leader with the pty as its
// controlling terminal, as a shell would run ethos.
func startUnderPty(t *testing.T, git string) (*exec.Cmd, chan struct{}) {
	t.Helper()
	_, slave := openPty(t)
	c := exec.Command(os.Args[0], "-test.run=^TestHelperRunNetworkGit$")
	c.Env = append(os.Environ(), "ETHOS_HELPER_GIT="+git)
	c.Stdin = slave
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	require.NoError(t, c.Start())
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	return c, done
}

// A network git has no controlling terminal: the user's own ssh, which reads
// /dev/tty to ask for a host key or a passphrase, fails at once, with nothing
// left stopped on the terminal.
func TestNetworkGitHasNoControllingTerminal(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "tty")
	ssh := filepath.Join(dir, "my-ssh")
	require.NoError(t, os.WriteFile(ssh, []byte("#!/bin/sh\nif (: </dev/tty) 2>/dev/null; then echo tty > '"+out+"'; else echo notty > '"+out+"'; fi\n"), 0o755))
	git := fakeGitScript(t, "case \"$1\" in config) exit 1;; esac\nexec '"+ssh+"'\n")
	t.Setenv("GIT_SSH_COMMAND", "my own ssh") // the user's choice: kept, and still cannot prompt

	start := time.Now()
	_, done := startUnderPty(t, git)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the network git waited on the terminal")
	}
	assert.Less(t, time.Since(start), 5*time.Second)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "notty\n", string(data), "the child of a network git must not have /dev/tty")

}

// A hangup of ethos's terminal stops a network git, its ssh, and removes the
// target a clone had begun (git clone's own signal handler does that on
// SIGTERM; the fake does the same).
func TestHangupLeavesNoCloneRunning(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "ssh.pid")
	target := filepath.Join(dir, "target")
	git := fakeGitScript(t, "case \"$1\" in config) exit 1;; esac\n"+
		"mkdir '"+target+"'\ntrap 'rm -rf \""+target+"\"; kill $!; exit 143' TERM\n"+
		"sh -c \"echo \\$\\$ > '"+pidFile+"'; exec sleep 60\" &\nwait\n")
	c, done := startUnderPty(t, git)
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return pid > 0
	}, 5*time.Second, 20*time.Millisecond)
	require.DirExists(t, target)

	// The hangup goes to ethos's group, as the terminal's does; the network
	// git is in a session of its own and gets it only through the forwarder.
	require.NoError(t, syscall.Kill(-c.Process.Pid, syscall.SIGHUP))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the helper did not exit on SIGHUP")
	}
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 3*time.Second, 50*time.Millisecond, "the ssh (pid %d) outlived the hangup", pid)
	assert.NoDirExists(t, target, "the clone's target is gone")
}
