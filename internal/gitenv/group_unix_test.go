//go:build unix

package gitenv

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

// spyGit is a fake git that logs every call (its arguments, one line each, to the
// returned file) and exits 1 for `config core.sshCommand`, as git does for an unset
// key. Anything else prints its arguments and the variables under test.
func spyGit(t *testing.T) (git, log string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "calls.log")
	git = fakeGitScript(t, "echo \"$*\" >> '"+log+"'\n"+
		"for a in \"$@\"; do [ \"$a\" = core.sshCommand ] && exit 1; done\n"+
		"echo \"$GIT_TERMINAL_PROMPT|$GIT_ASKPASS|$SSH_ASKPASS|$SSH_ASKPASS_REQUIRE|$GIT_SSH_COMMAND|$GIT_SSH\"\n")
	return git, log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// A Network git cannot prompt, and ethos sets no ssh variable and reads no ssh
// setting: the only git call is the clone itself (no `config core.sshCommand`, no
// ls-remote), GIT_SSH_COMMAND is not invented, and the user's own GIT_SSH_COMMAND
// or GIT_SSH reaches git untouched. A reintroduced BatchMode or core.sshCommand
// read fails this.
func TestNetworkGitCannotPrompt(t *testing.T) {
	git, log := spyGit(t)
	t.Setenv("GIT_ASKPASS", "/some/askpass")
	t.Setenv("SSH_ASKPASS", "/some/ssh-askpass")
	require.NoError(t, os.Unsetenv("GIT_SSH_COMMAND"))
	require.NoError(t, os.Unsetenv("GIT_SSH"))
	run := func() string {
		require.NoError(t, os.Remove(log))
		out, err := Run(Spec{Git: git, Network: true, Args: []string{"clone", "u"}})
		require.NoError(t, err)
		got := calls(t, log)
		require.Len(t, got, 1, "the only git call is the clone, not a config or ls-remote look: %v", got)
		assert.Equal(t, "-c core.askPass= -c credential.interactive=never clone u", got[0])
		return strings.TrimSpace(string(out))
	}
	// A first run creates the log; run() removes it, so make it exist.
	require.NoError(t, os.WriteFile(log, nil, 0o644))

	assert.Equal(t, "0|||never||", run(), "no askpass runs, and no ssh variable is set")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /my/key")
	assert.Equal(t, "0|||never|ssh -i /my/key|", run(), "the user's GIT_SSH_COMMAND is untouched")
	require.NoError(t, os.Unsetenv("GIT_SSH_COMMAND"))
	t.Setenv("GIT_SSH", "/my/ssh")
	assert.Equal(t, "0|||never||/my/ssh", run(), "the user's GIT_SSH is untouched")

	require.NoError(t, os.Unsetenv("GIT_SSH"))
	out, err := Run(Spec{Git: git, Args: []string{"clone", "u"}})
	require.NoError(t, err)
	assert.Equal(t, "|/some/askpass|/some/ssh-askpass|||", strings.TrimSpace(string(out)), "an ordinary git is untouched")
}

// A network git reads no user config of its own: with one it cannot read, it still
// runs, and makes no `config` call at all (the fake git exits 1 for core.sshCommand
// and logs every call). A reintroduced read fails this, by the log and by the
// refusal an unreadable config used to cause.
func TestNetworkGitDoesNotReadTheUserConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(cfg, []byte("[core]\n\tsshCommand = ssh -i key\n"), 0o644))
	require.NoError(t, os.Chmod(cfg, 0o000))
	t.Cleanup(func() { _ = os.Chmod(cfg, 0o644) })
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	require.NoError(t, os.Unsetenv("GIT_SSH_COMMAND"))
	git, log := spyGit(t)
	out, err := Run(Spec{Git: git, Network: true, Args: []string{"clone", "u"}})
	require.NoError(t, err)
	got := calls(t, log)
	require.Len(t, got, 1, "no config or ls-remote call: %v", got)
	for _, line := range got {
		assert.NotContains(t, line, "core.sshCommand")
		assert.NotContains(t, line, "config ")
		assert.NotContains(t, line, "ls-remote")
	}
	assert.NotContains(t, string(out), "BatchMode")
	fields := strings.Split(strings.TrimSpace(string(out)), "|")
	require.Len(t, fields, 6)
	assert.Empty(t, fields[4], "GIT_SSH_COMMAND is not set")
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
	limit := 30 * time.Second
	if v := os.Getenv("ETHOS_HELPER_LIMIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		limit = d
	}
	_, _ = Run(Spec{Git: git, Network: os.Getenv("ETHOS_HELPER_NETWORK") == "1", Limit: limit})
}

// TestHelperDieOf is the child of the ignored-signal tests, not a test: it
// calls dieOf(SIGINT) and must not return.
func TestHelperDieOf(t *testing.T) {
	if os.Getenv("ETHOS_HELPER_DIE") == "" {
		t.Skip("helper process")
	}
	dieOf(syscall.SIGINT)
	os.Exit(99)
}

// startHelper runs the helper test named by run with SIGINT inherited as
// ignored, as in a background job.
func startHelper(t *testing.T, run string, env ...string) *exec.Cmd {
	t.Helper()
	c := exec.Command("sh", "-c", `trap '' INT; exec "$0" -test.run="$1"`, os.Args[0], run)
	c.Env = append(os.Environ(), env...)
	return c
}

func waitFor(t *testing.T, c *exec.Cmd, d time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		_ = c.Process.Kill()
		t.Fatal("the helper did not exit in time")
		return nil
	}
}

// dieOf does not hang when the signal it raises is ignored: it ends the process
// with 128 plus the signal number.
func TestDieOfIgnoredSignalExits(t *testing.T) {
	c := startHelper(t, "^TestHelperDieOf$", "ETHOS_HELPER_DIE=1")
	require.NoError(t, c.Start())
	err := waitFor(t, c, 5*time.Second)
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 128+int(syscall.SIGINT), ee.ExitCode())
}

// A SIGINT inherited as ignored stays ignored: Run does not catch it, the git
// finishes, and Run returns normally.
func TestRunHonoursInheritedIgnoredSIGINT(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "git.pid")
	git := fakeGitScript(t, "echo $$ > '"+pidFile+"'\nsleep 1\n")
	c := startHelper(t, "^TestHelperRunGit$", "ETHOS_HELPER_GIT="+git)
	require.NoError(t, c.Start())
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		return err == nil && len(strings.TrimSpace(string(data))) > 0
	}, 5*time.Second, 20*time.Millisecond, "the git started")
	require.NoError(t, c.Process.Signal(syscall.SIGINT))
	require.NoError(t, waitFor(t, c, 5*time.Second), "the helper ignored SIGINT and exited normally")
}

// A signal that arrives while a timed-out git is being reaped ends ethos with
// that signal; it is not swallowed into ErrTimeout. The fake git ignores TERM, so
// reaping lasts until WaitDelay (a second) after the 300ms limit.
func TestSignalDuringTimeoutReapEndsTheProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "git.pid")
	git := fakeGitScript(t, "trap '' TERM\necho $$ > '"+pidFile+"'\nsleep 60\n")
	c := exec.Command(os.Args[0], "-test.run=^TestHelperRunGit$")
	c.Env = append(os.Environ(), "ETHOS_HELPER_GIT="+git, "ETHOS_HELPER_LIMIT=300ms")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, c.Start())
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return pid > 0
	}, 5*time.Second, 20*time.Millisecond, "the git started")
	defer func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }()
	time.Sleep(600 * time.Millisecond) // past the limit, before the kill
	require.NoError(t, c.Process.Signal(syscall.SIGTERM))
	err := waitFor(t, c, 10*time.Second)
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	ws, ok := ee.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	assert.True(t, ws.Signaled() && ws.Signal() == syscall.SIGTERM, "ended by SIGTERM, got %v", ee)
}

// runHelperAndInterrupt starts the helper in its own process group (the
// terminal's foreground group), waits for the fake git to report its pid, sends
// sig to the whole group as Ctrl-C does for SIGINT, and returns the git's pid.
func runHelperAndInterrupt(t *testing.T, network bool, sig syscall.Signal) int {
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
	require.NoError(t, syscall.Kill(-c.Process.Pid, sig))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the helper did not exit on the signal")
	}
	return pid
}

// A terminating signal does not leave any git running, Network or ordinary:
// ethos stops git's whole process group, which the terminal's signal does not
// reach (git is in a group of its own), so no git is left holding index.lock.
func TestSignalDoesNotLeaveAGitRunning(t *testing.T) {
	for _, network := range []bool{true, false} {
		for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			t.Run(fmt.Sprintf("network=%v/%v", network, sig), func(t *testing.T) {
				pid := runHelperAndInterrupt(t, network, sig)
				defer func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }()
				// git is its group's leader, so its group is gone when kill(-pid) finds nobody.
				require.Eventually(t, func() bool { return errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) }, 3*time.Second, 50*time.Millisecond,
					"git (pid %d) or its group outlived %v", pid, sig)
			})
		}
	}
}
