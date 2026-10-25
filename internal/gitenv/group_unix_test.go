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

// A Network git cannot prompt, and keeps the user's ssh choice.
func TestNetworkGitCannotPrompt(t *testing.T) {
	git := fakeGitScript(t, `case "$1" in config) [ -n "$FAKE_SSHCMD" ] && { echo "$FAKE_SSHCMD"; exit 0; }; exit 1;; esac
echo "$@|$GIT_TERMINAL_PROMPT|$GIT_ASKPASS|$SSH_ASKPASS|$SSH_ASKPASS_REQUIRE|$GIT_SSH_COMMAND"
`)
	t.Setenv("GIT_ASKPASS", "/some/askpass")
	t.Setenv("SSH_ASKPASS", "/some/ssh-askpass")
	run := func() string {
		out, err := Run(Spec{Git: git, Network: true, Args: []string{"clone", "u"}})
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}
	const prefix = "-c core.askPass= -c credential.interactive=never clone u|0|||never|"

	assert.Equal(t, prefix+"ssh -o BatchMode=yes", run(), "no ssh chosen: BatchMode, and no askpass program runs")

	t.Setenv("GIT_SSH_COMMAND", "ssh -i /my/key")
	assert.Equal(t, prefix+"ssh -i /my/key", run(), "the user's GIT_SSH_COMMAND is kept")

	t.Setenv("GIT_SSH_COMMAND", "")
	require.NoError(t, os.Unsetenv("GIT_SSH_COMMAND"))
	t.Setenv("GIT_SSH", "/my/ssh")
	assert.Equal(t, prefix, run(), "GIT_SSH is the user's choice: no GIT_SSH_COMMAND is added")

	require.NoError(t, os.Unsetenv("GIT_SSH"))
	t.Setenv("FAKE_SSHCMD", "ssh -F /my/config")
	assert.Equal(t, prefix, run(), "core.sshCommand is the user's choice: no GIT_SSH_COMMAND is added")

	out, err := Run(Spec{Git: git, Args: []string{"clone", "u"}})
	require.NoError(t, err)
	assert.Equal(t, "clone u||/some/askpass|/some/ssh-askpass||", strings.TrimSpace(string(out)), "an ordinary git is untouched")
}

// At the deadline a Network git's descendants (the ssh it started) are dead
// too, whether or not they ignore SIGTERM.
func TestNetworkGitTimeoutKillsItsDescendants(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "ssh.pid")
	git := fakeGitScript(t, `case "$1" in config) exit 1;; esac
trap '' TERM
sh -c "trap '' TERM; echo \$\$ > '`+pidFile+`'; exec sleep 60" &
while [ ! -s '`+pidFile+`' ]; do sleep 0.05; done
wait
`)
	_, err := Run(Spec{Git: git, Network: true, Args: []string{"clone", "u"}, Limit: 500 * time.Millisecond})
	require.ErrorIs(t, err, ErrTimeout)
	data, rerr := os.ReadFile(pidFile)
	require.NoError(t, rerr)
	pid, aerr := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, aerr)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 3*time.Second, 50*time.Millisecond,
		"the ssh (pid %d) outlived the deadline", pid)
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
	git := fakeGitScript(t, "case \"$1\" in config) exit 1;; esac\necho $$ > '"+pidFile+"'\nexec sleep 60\n")
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

// Ctrl-C does not leave a Network git running: ethos forwards the signal to its
// group. An ordinary git has no forwarder, and is in its own group, so it is
// not reached (it is bounded by its timeout instead).
func TestCtrlCDoesNotLeaveANetworkGitRunning(t *testing.T) {
	pid := runHelperAndInterrupt(t, true)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 3*time.Second, 50*time.Millisecond,
		"the network git (pid %d) outlived Ctrl-C", pid)

	pid = runHelperAndInterrupt(t, false)
	assert.NoError(t, syscall.Kill(pid, 0), "an ordinary git runs in its own group and is not reached by a terminal's SIGINT")
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
