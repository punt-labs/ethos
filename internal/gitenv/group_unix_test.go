//go:build unix

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
)

// On a timeout the whole process group dies: a clean or smudge filter that git
// started, and that ignores TERM, does not outlive git.
func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "filter.pid")
	git := fakeGitScript(t, "trap '' TERM\nsh -c \"trap '' TERM; echo \\$\\$ > '"+pidFile+"'; exec sleep 60\" &\nwhile [ ! -s '"+pidFile+"' ]; do sleep 0.05; done\nwait\n")
	testhelpersTimeout(t, 500*time.Millisecond)
	_, err := OutputEnv(dir, git, nil, "add")
	require.ErrorIs(t, err, ErrTimeout)
	data, rerr := os.ReadFile(pidFile)
	require.NoError(t, rerr)
	pid, aerr := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, aerr)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 3*time.Second, 50*time.Millisecond,
		"the filter (pid %d) outlived git", pid)
}

// After SIGTERM the whole group gets the grace to finish, not only git: a
// filter that needs 400ms to clean up is not killed the moment git exits.
func TestTimeoutGivesTheGroupItsGrace(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "cleaned")
	git := fakeGitScript(t, "(trap 'sleep 0.4; echo cleaned > '\""+marker+"\"'; exit 0' TERM; sleep 30 & wait) >/dev/null 2>&1 &\ntrap 'exit 0' TERM\nsleep 30 &\nwait\n")
	testhelpersTimeout(t, 300*time.Millisecond)
	_, err := OutputEnv(dir, git, nil, "add")
	require.ErrorIs(t, err, ErrTimeout)
	require.Eventually(t, func() bool { _, serr := os.Stat(marker); return serr == nil },
		2*time.Second, 50*time.Millisecond, "the filter finished its cleanup before it was killed")
}

// A Network git has no credential prompt to wait on.
func TestNetworkGitCannotPrompt(t *testing.T) {
	git := fakeGitScript(t, "echo \"$GIT_TERMINAL_PROMPT|$GIT_SSH_COMMAND\"\n")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /some/key")
	out, err := Run(Spec{Git: git, Network: true})
	require.NoError(t, err)
	assert.Equal(t, "0|ssh -o BatchMode=yes\n", string(out))
	out, err = Run(Spec{Git: git})
	require.NoError(t, err)
	assert.Equal(t, "|ssh -i /some/key\n", string(out), "an ordinary git keeps its own environment")
}

// TestHelperRunGit is the child of the Ctrl-C tests, not a test: it runs one
// git through Run and exits.
func TestHelperRunGit(t *testing.T) {
	git := os.Getenv("ETHOS_HELPER_GIT")
	if git == "" {
		t.Skip("helper process")
	}
	_, _ = Run(Spec{Git: git, Network: os.Getenv("ETHOS_HELPER_NETWORK") == "1", Limit: 30 * time.Second})
}

// runHelperAndInterrupt starts the helper in its own process group (the
// terminal's foreground group), waits for the fake git to report its pid, sends
// SIGINT to the whole group as Ctrl-C does, and returns the git's pid.
func runHelperAndInterrupt(t *testing.T, network bool) int {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "git.pid")
	git := fakeGitScript(t, "echo $$ > '"+pidFile+"'\nexec sleep 60\n")
	c := exec.Command(os.Args[0], "-test.run=^TestHelperRunGit$")
	c.Env = append(os.Environ(), "ETHOS_HELPER_GIT="+git)
	if network {
		c.Env = append(c.Env, "ETHOS_HELPER_NETWORK=1")
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, c.Start())
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return pid > 0
	}, 5*time.Second, 20*time.Millisecond, "the git started")
	require.NoError(t, syscall.Kill(-c.Process.Pid, syscall.SIGINT))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the helper did not exit on SIGINT")
	}
	return pid
}

// Ctrl-C reaches a Network git, so no clone is left running; an ordinary git
// is in its own group and is not interrupted by it (it is bounded by its
// timeout instead).
func TestCtrlCDoesNotLeaveANetworkGitRunning(t *testing.T) {
	pid := runHelperAndInterrupt(t, true)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 3*time.Second, 50*time.Millisecond,
		"the network git (pid %d) outlived Ctrl-C", pid)

	pid = runHelperAndInterrupt(t, false)
	assert.NoError(t, syscall.Kill(pid, 0), "an ordinary git runs in its own group and is not reached by a terminal's SIGINT")
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
