//go:build linux || darwin

package hooks

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A git that fails `rev-parse --show-toplevel` (dubious ownership, a corrupt
// .git) must not make the hooks skip silently with the host's status. Each
// prints git's own error to stderr and exits 1, so the commit is blocked and
// the operator sees why.
func TestHooksFailLoudWhenRevParseFails(t *testing.T) {
	for _, hook := range []string{"pre-commit.sh", "commit-msg.sh"} {
		t.Run(hook, func(t *testing.T) {
			bin := t.TempDir()
			fake := "#!/bin/sh\necho 'fatal: detected dubious ownership in repository at /x' >&2\nexit 128\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755))
			msg := filepath.Join(t.TempDir(), "MSG")
			require.NoError(t, os.WriteFile(msg, []byte("subject\n"), 0o644))

			cmd := exec.Command("/bin/sh", hook, msg)
			cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + bin + ":/usr/bin:/bin"}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, "the hook must fail, not exit with the host status 0")
			assert.Equal(t, 1, ee.ExitCode())
			assert.Contains(t, stderr.String(), "dubious ownership", "git's own error is shown")
			assert.Contains(t, stderr.String(), hook[:len(hook)-3], "the hook names itself")
		})
	}
}

// fakeGit writes a git script into a new directory and returns it, for PATH.
func fakeGitDir(t *testing.T, body string) string {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+body), 0o755))
	return bin
}

// A rev-parse that succeeds but writes to stderr (a warning, GIT_TRACE) must
// not be read as a failure or have its noise folded into the repository root:
// the hook still seals.
func TestPreCommitSealsDespiteRevParseStderrNoise(t *testing.T) {
	for name, body := range map[string]string{
		"a warning": `[ "$1" = rev-parse ] && { echo "warning: something harmless" >&2; echo "$REPO"; exit 0; }` + "\nexit 0\n",
		"GIT_TRACE": `[ "$1" = rev-parse ] && { echo "trace: built-in: git rev-parse --show-toplevel" >&2; echo "$REPO"; exit 0; }` + "\nexit 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), nil, 0o644))
			bin := fakeGitDir(t, body)
			marker := filepath.Join(t.TempDir(), "sealed")
			require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755))

			cmd := exec.Command("/bin/sh", "pre-commit.sh")
			cmd.Env = []string{"HOME=" + t.TempDir(), "REPO=" + repo, "PATH=" + bin + ":/usr/bin:/bin"}
			require.NoError(t, cmd.Run())
			assert.FileExists(t, marker, "the seal ran")
		})
	}
}

// When git interpret-trailers fails, commit-msg prints git's error and exits
// 1: the trailer is never silently replaced by a plain append.
func TestCommitMsgInterpretTrailersFailureBlocks(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), nil, 0o644))
	bin := fakeGitDir(t, `case "$1" in
rev-parse) echo "$REPO"; exit 0;;
interpret-trailers) echo "fatal: interpret-trailers exploded" >&2; exit 128;;
esac
exit 0
`)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	msg := filepath.Join(t.TempDir(), "MSG")
	require.NoError(t, os.WriteFile(msg, []byte("subject\n"), 0o644))

	cmd := exec.Command("/bin/sh", "commit-msg.sh", msg)
	cmd.Env = []string{"HOME=" + t.TempDir(), "REPO=" + repo, "MISSION_ID=m-2026-01-01-001", "PATH=" + bin + ":/usr/bin:/bin"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 1, ee.ExitCode())
	assert.Contains(t, stderr.String(), "interpret-trailers exploded")
	got, rerr := os.ReadFile(msg)
	require.NoError(t, rerr)
	assert.Equal(t, "subject\n", string(got), "no trailer was appended behind git's back")
}

// shimMktemp puts a mktemp that runs body ahead of the real one.
func shimMktemp(t *testing.T, bin, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "mktemp"), []byte("#!/bin/sh\n"+body), 0o755))
}

// A mktemp that fails must not block a repository where ethos is dormant, and
// must not hide a rev-parse failure: the hook runs rev-parse without the
// capture file and shows the error only when it fails.
func TestHooksSurviveMktempFailure(t *testing.T) {
	for _, hook := range []string{"pre-commit.sh", "commit-msg.sh"} {
		t.Run(hook+"/dormant", func(t *testing.T) {
			repo := t.TempDir() // no .punt-labs/ethos/enabled
			bin := fakeGitDir(t, "echo \"$REPO\"\n")
			shimMktemp(t, bin, "exit 1\n")
			msg := filepath.Join(t.TempDir(), "MSG")
			require.NoError(t, os.WriteFile(msg, []byte("subject\n"), 0o644))
			cmd := exec.Command("/bin/sh", hook, msg)
			cmd.Env = []string{"HOME=" + t.TempDir(), "REPO=" + repo, "PATH=" + bin + ":/usr/bin:/bin"}
			require.NoError(t, cmd.Run(), "a dormant repository is not blocked by a failing mktemp")
		})
		t.Run(hook+"/rev-parse fails", func(t *testing.T) {
			bin := fakeGitDir(t, "echo 'fatal: detected dubious ownership in repository at /x' >&2\nexit 128\n")
			shimMktemp(t, bin, "exit 1\n")
			msg := filepath.Join(t.TempDir(), "MSG")
			require.NoError(t, os.WriteFile(msg, []byte("subject\n"), 0o644))
			cmd := exec.Command("/bin/sh", hook, msg)
			cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + bin + ":/usr/bin:/bin"}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee)
			assert.Equal(t, 1, ee.ExitCode())
			assert.Contains(t, stderr.String(), "dubious ownership")
		})
	}
}

// In a repository where ethos is enabled, a missing ethos binary is not a
// quiet pass: the commit would carry unsealed audit lines.
func TestPreCommitFailsWhenEnabledAndEthosMissing(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), nil, 0o644))
	bin := fakeGitDir(t, "echo \"$REPO\"\n")
	cmd := exec.Command("/bin/sh", "pre-commit.sh")
	cmd.Env = []string{"HOME=" + t.TempDir(), "REPO=" + repo, "PATH=" + bin + ":/usr/bin:/bin"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 1, ee.ExitCode())
	assert.Contains(t, stderr.String(), "ethos is enabled here but not installed; the commit is not sealed")
}

// A redirect that fails is reported as one, not as git interpret-trailers
// failing with a message nobody wrote; the trailer still lands by plain append.
func TestCommitMsgRedirectFailureIsReportedAsSuch(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), nil, 0o644))
	bin := fakeGitDir(t, `case "$1" in
rev-parse) echo "$REPO"; exit 0;;
interpret-trailers) echo "git must not run"; exit 0;;
esac
exit 0
`)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	// The temp file is made, and its .err sibling cannot be: a directory is there.
	shimMktemp(t, bin, `f=$(/usr/bin/mktemp "$@") || exit 1
mkdir "$f.err" || exit 1
echo "$f"
`)
	msg := filepath.Join(t.TempDir(), "MSG")
	require.NoError(t, os.WriteFile(msg, []byte("subject\n"), 0o644))
	cmd := exec.Command("/bin/sh", "commit-msg.sh", msg)
	cmd.Env = []string{"HOME=" + t.TempDir(), "REPO=" + repo, "MISSION_ID=m-2026-01-01-001", "PATH=" + bin + ":/usr/bin:/bin"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), stderr.String())
	assert.Contains(t, stderr.String(), "cannot create")
	assert.NotContains(t, stderr.String(), "interpret-trailers failed")
	got, err := os.ReadFile(msg)
	require.NoError(t, err)
	assert.Contains(t, string(got), "Mission: m-2026-01-01-001", "the trailer landed by plain append")
}

// realRepoWithHooks is a real repository with both hooks installed, a commit
// ready to make, and a PATH that holds git and no ethos.
func realRepoWithHooks(t *testing.T) (repo string, run func(args ...string) (string, error)) {
	t.Helper()
	repo = t.TempDir()
	home := t.TempDir()
	env := append(testhelpers.GitEnv(),
		"HOME="+home, "PATH=/usr/bin:/bin", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	run = func(args ...string) (string, error) {
		c := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-C", repo}, args...)...)
		c.Env = env
		out, err := c.CombinedOutput()
		return string(out), err
	}
	if out, err := run("init", "-q", "-b", "main"); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for _, h := range []string{"pre-commit", "commit-msg"} {
		body, err := os.ReadFile(h + ".sh")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "hooks", h), body, 0o755))
	}
	return repo, run
}

// A .punt-labs or .punt-labs/ethos that git cannot follow is not "ethos is
// dormant": a real commit is blocked and says why.
func TestHooksBlockOnAnUnreadableStorePath(t *testing.T) {
	cases := map[string]func(t *testing.T, repo string){
		"dangling .punt-labs": func(t *testing.T, repo string) {
			require.NoError(t, os.Symlink(filepath.Join(repo, "nowhere"), filepath.Join(repo, ".punt-labs")))
		},
		"dangling .punt-labs/ethos": func(t *testing.T, repo string) {
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs"), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(repo, "nowhere"), filepath.Join(repo, ".punt-labs", "ethos")))
		},
		".punt-labs/ethos is a file": func(t *testing.T, repo string) {
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos"), []byte("x"), 0o644))
		},
	}
	cases["marker is a directory"] = func(t *testing.T, repo string) {
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), 0o755))
	}
	cases["marker is a FIFO"] = func(t *testing.T, repo string) {
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
		require.NoError(t, syscall.Mkfifo(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), 0o644))
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			repo, run := realRepoWithHooks(t)
			mk(t, repo)
			require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644))
			_, err := run("add", "a.txt")
			require.NoError(t, err)
			out, err := run("commit", "-m", "x")
			require.Error(t, err, "the commit is blocked: %s", out)
			assert.Contains(t, out, ".punt-labs")
			assert.True(t, strings.Contains(out, "dangling link") || strings.Contains(out, "not a directory") || strings.Contains(out, "not a regular file"), out)
		})
	}
	t.Run("really absent is dormant", func(t *testing.T) {
		repo, run := realRepoWithHooks(t)
		require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644))
		_, err := run("add", "a.txt")
		require.NoError(t, err)
		out, err := run("commit", "-m", "x")
		require.NoError(t, err, out)
	})
}

// Enabled with no ethos binary: git commit is blocked by pre-commit, and a
// merge (which runs commit-msg alone) by commit-msg, with the same words.
func TestHooksBlockWhenEnabledAndEthosMissing(t *testing.T) {
	repo, run := realRepoWithHooks(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644))
	_, err := run("add", "a.txt")
	require.NoError(t, err)
	// Commit the base with the hooks out of the way.
	require.NoError(t, os.Rename(filepath.Join(repo, ".git", "hooks", "pre-commit"), filepath.Join(repo, ".git", "pre-commit.off")))
	require.NoError(t, os.Rename(filepath.Join(repo, ".git", "hooks", "commit-msg"), filepath.Join(repo, ".git", "commit-msg.off")))
	out, err := run("commit", "-q", "-m", "base")
	require.NoError(t, err, out)
	require.NoError(t, os.Rename(filepath.Join(repo, ".git", "pre-commit.off"), filepath.Join(repo, ".git", "hooks", "pre-commit")))
	require.NoError(t, os.Rename(filepath.Join(repo, ".git", "commit-msg.off"), filepath.Join(repo, ".git", "hooks", "commit-msg")))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos", "enabled"), nil, 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(repo, "b.txt"), []byte("y"), 0o644))
	_, err = run("add", "b.txt")
	require.NoError(t, err)
	out, err = run("commit", "-m", "second")
	require.Error(t, err, out)
	assert.Contains(t, out, "ethos is enabled here but not installed; the commit is not sealed")

	// A merge that is not a fast-forward runs commit-msg and not pre-commit.
	_, err = run("reset", "-q", "--mixed")
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(repo, ".git", "hooks", "pre-commit")))
	_, _ = run("checkout", "-q", "-b", "side")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "c.txt"), []byte("z"), 0o644))
	_, _ = run("add", "c.txt")
	require.NoError(t, os.Rename(filepath.Join(repo, ".git", "hooks", "commit-msg"), filepath.Join(repo, ".git", "commit-msg.off")))
	out, err = run("commit", "-q", "-m", "side work")
	require.NoError(t, err, out)
	require.NoError(t, os.Rename(filepath.Join(repo, ".git", "commit-msg.off"), filepath.Join(repo, ".git", "hooks", "commit-msg")))
	_, _ = run("checkout", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "d.txt"), []byte("w"), 0o644))
	_, _ = run("add", "d.txt")
	require.NoError(t, os.Rename(filepath.Join(repo, ".git", "hooks", "commit-msg"), filepath.Join(repo, ".git", "commit-msg.off")))
	out, err = run("commit", "-q", "-m", "main work")
	require.NoError(t, err, out)
	require.NoError(t, os.Rename(filepath.Join(repo, ".git", "commit-msg.off"), filepath.Join(repo, ".git", "hooks", "commit-msg")))
	out, err = run("merge", "--no-ff", "-m", "merge", "side")
	require.Error(t, err, "the merge is blocked by commit-msg: %s", out)
	assert.Contains(t, out, "commit-msg: ethos is enabled here but not installed; the commit is not sealed")
}
