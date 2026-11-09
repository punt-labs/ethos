//go:build linux || darwin

package hooks

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// These tests run a real `git commit` through the installed DES-083 shims
// against a real binary, never a stub: dormant, enabled with each kind of
// failure, traced, with no binary, with an old binary, and from a section
// copied before the shim existed.

// oldBinaryRev is the last commit before the shim subcommands: its binary has
// no `hook pre-commit` or `hook commit-msg`.
const oldBinaryRev = "93c9f82"

var (
	oldOnce sync.Once
	oldDir  string
	oldErr  error
)

// oldBinDir builds the ethos binary at oldBinaryRev once and returns its
// directory. The source is exported with git archive into a scratch directory,
// so the checkout under development is never touched.
func oldBinDir(t *testing.T) string {
	t.Helper()
	oldOnce.Do(func() {
		root, err := filepath.Abs("../..")
		if err != nil {
			oldErr = err
			return
		}
		src, err := os.MkdirTemp("", "ethos-old-src")
		if err != nil {
			oldErr = err
			return
		}
		defer os.RemoveAll(src)
		oldDir, err = os.MkdirTemp("", "ethos-old-bin")
		if err != nil {
			oldErr = err
			return
		}
		archive := testhelpers.GitCommand("-C", root, "archive", oldBinaryRev)
		tarball, err := archive.Output()
		if err != nil {
			oldErr = fmt.Errorf("git archive %s: %w", oldBinaryRev, err)
			return
		}
		untar := exec.Command("tar", "-x", "-C", src)
		untar.Stdin = bytes.NewReader(tarball)
		if out, err := untar.CombinedOutput(); err != nil {
			oldErr = fmt.Errorf("extracting %s: %w\n%s", oldBinaryRev, err, out)
			return
		}
		build := exec.Command("go", "build", "-o", filepath.Join(oldDir, "ethos"), "./cmd/ethos")
		build.Dir = src
		if out, err := build.CombinedOutput(); err != nil {
			oldErr = fmt.Errorf("building %s: %w\n%s", oldBinaryRev, err, out)
		}
	})
	if oldErr != nil {
		t.Skipf("cannot build the binary at %s (a shallow clone has no such commit): %v", oldBinaryRev, oldErr)
	}
	return oldDir
}

// shimRepo is a repository with the shims installed by `ethos enable`.
type shimRepo struct {
	dir, home string
	env       []string
}

// newShimRepo enables ethos in a fresh repository with bin first on PATH. An
// empty bin leaves no ethos reachable once enable has installed the hooks.
func newShimRepo(t *testing.T, enable bool) *shimRepo {
	t.Helper()
	r := &shimRepo{dir: t.TempDir(), home: t.TempDir()}
	r.env = append(testhelpers.GitEnv(),
		"HOME="+r.home,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
		"PATH=/usr/bin:/bin")
	runGit(t, r.dir, "init", "-q", "-b", "main")
	if enable {
		cmd := exec.Command(filepath.Join(ethosBinDir(t), "ethos"), "enable")
		cmd.Dir, cmd.Env = r.dir, r.env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "enable: %s", out)
	} else {
		for _, h := range []string{"pre-commit", "commit-msg"} {
			body, err := os.ReadFile(h + ".sh")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(r.dir, ".git", "hooks", h), body, 0o755))
		}
	}
	return r
}

// withPath returns the environment with PATH replaced by dirs and the base.
func (r *shimRepo) withPath(dirs ...string) []string {
	return append(append([]string{}, r.env...), "PATH="+strings.Join(append(dirs, "/usr/bin", "/bin"), ":"))
}

// commit stages a file and runs `git commit` under env, returning the combined
// output and the error.
func (r *shimRepo) commit(t *testing.T, env []string, msg string) (string, error) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "f.txt"), []byte(msg), 0o644))
	for _, args := range [][]string{{"add", "f.txt"}, {"-c", "commit.gpgsign=false", "commit", "-q", "-m", msg}} {
		cmd := testhelpers.GitCommand(append([]string{"-C", r.dir}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil || args[0] != "add" {
			return string(out), err
		}
	}
	return "", nil
}

func (r *shimRepo) lastMessage(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "-C", r.dir, "log", "-1", "--format=%B")
	cmd.Env = []string{"HOME=" + r.home, "PATH=/usr/bin:/bin", "GIT_TRACE=0", "GIT_TRACE2=0", "GIT_TRACE2_PERF=0", "GIT_TRACE2_EVENT=0"}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

func (r *shimRepo) removeBinaryFromEverywhere(t *testing.T) {
	t.Helper()
	require.NoError(t, os.RemoveAll(filepath.Join(r.home, ".local")))
}

// A dormant repository with the shims installed never reaches the binary: the
// PATH has no ethos and the commit still succeeds.
func TestShimDormantNeverStartsTheBinary(t *testing.T) {
	r := newShimRepo(t, false)
	out, err := r.commit(t, r.withPath(), "dormant")
	require.NoError(t, err, out)
	assert.NotContains(t, out, "ethos")
	assert.Equal(t, "dormant\n\n", r.lastMessage(t))
}

// Enabled with a malformed policy file: pre-commit blocks with exit 2 and the
// message names the configuration.
func TestShimEnabledInvalidConfigBlocks(t *testing.T) {
	r := newShimRepo(t, true)
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, ".punt-labs", "ethos.yaml"), []byte("record: [invalid\n"), 0o644))
	out, err := r.commit(t, r.withPath(ethosBinDir(t)), "invalid config")
	require.Error(t, err, out)
	assert.Contains(t, out, "ethos.yaml")
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 1, ee.ExitCode(), "git reports a blocked commit as 1; the hook's own exit 2 is checked below")

	// Run the hook itself for its exit status.
	cmd := exec.Command(filepath.Join(r.dir, ".git", "hooks", "pre-commit"))
	cmd.Dir, cmd.Env = r.dir, r.withPath(ethosBinDir(t))
	hookOut, herr := cmd.CombinedOutput()
	require.ErrorAs(t, herr, &ee, string(hookOut))
	assert.Equal(t, 2, ee.ExitCode())
}

// Enabled under record: blame with an archive that cannot be written: the
// commit succeeds with the retained-data warning and the live data stays.
func TestShimEnabledBlameArchivalErrorWarns(t *testing.T) {
	r := newShimRepo(t, true)
	p, err := layout.NewRepository(r.dir, r.dir, layout.RecordBlame)
	require.NoError(t, err)
	// A corrupt sealed chunk is the real blame archival error: it makes the
	// seal fail while a healthy session still has live lines.
	damaged := audit.Stream{Namespace: audit.SessionNS, Session: "fixture"}
	dir, err := audit.ArchiveDir(p, damaged, "2026-11-04")
	require.NoError(t, err)
	require.NoError(t, audit.WriteLocalAtomic(p, filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte("corrupt\n")))
	healthy := audit.Stream{Namespace: audit.SessionNS, Session: "z-new"}
	line := `{"ts":"` + audit.FormatLineTS(1000) + `","session":"z-new","tool":"Read","record":"blame"}` + "\n"
	require.NoError(t, audit.WriteLocalAtomic(p, healthy.LivePath(p), []byte(line)))

	out, err := r.commit(t, r.withPath(ethosBinDir(t)), "blame warning")
	require.NoError(t, err, out)
	assert.Contains(t, out, "retained-data warning")
	assert.Equal(t, "blame warning\n\n", r.lastMessage(t))
}

// Enabled with an active mission in the environment: both trailers reach git log.
func TestShimEnabledMissionTrailers(t *testing.T) {
	r := newShimRepo(t, true)
	env := append(r.withPath(ethosBinDir(t)), "MISSION_ID=m-2026-11-04-043", "DELEGATION_ID=d-2026-11-04-043")
	out, err := r.commit(t, env, "with mission")
	require.NoError(t, err, out)
	assert.Equal(t, "with mission\n\nMission: m-2026-11-04-043\nDelegation: d-2026-11-04-043\n\n", r.lastMessage(t))
}

// A trace aimed at stdout changes neither the outcome nor the stored message,
// for both hooks: the binary, not the shim, now owns every git call it makes.
func TestShimEnabledGitTraceChangesNothing(t *testing.T) {
	r := newShimRepo(t, true)
	plain := append(r.withPath(ethosBinDir(t)), "MISSION_ID=m-2026-11-04-043")
	out, err := r.commit(t, plain, "plain")
	require.NoError(t, err, out)
	want := strings.Replace(r.lastMessage(t), "plain", "traced", 1)

	traced := append(plain, "GIT_TRACE=/dev/stdout", "GIT_TRACE2=/dev/stdout", "GIT_TRACE2_EVENT=/dev/stdout")
	out, err = r.commit(t, traced, "traced")
	require.NoError(t, err, out)
	assert.NotContains(t, out, "ethos:", "the hooks print nothing on success")
	assert.Equal(t, want, r.lastMessage(t))
}

// Enabled with no binary on PATH or in $HOME/.local/bin: both hooks block and
// say so.
func TestShimEnabledNoBinaryBlocks(t *testing.T) {
	r := newShimRepo(t, true)
	r.removeBinaryFromEverywhere(t)
	out, err := r.commit(t, r.withPath(), "no binary")
	require.Error(t, err, out)
	assert.Contains(t, out, "pre-commit: ethos is enabled here but not installed")

	// commit-msg alone, as `git merge --no-ff` runs it.
	require.NoError(t, os.Remove(filepath.Join(r.dir, ".git", "hooks", "pre-commit")))
	out, err = r.commit(t, r.withPath(), "no binary again")
	require.Error(t, err, out)
	assert.Contains(t, out, "commit-msg: ethos is enabled here but not installed")
}

// Enabled with a binary built before the subcommands existed: both hooks block
// (fail closed) and the message names the remedy.
func TestShimEnabledOldBinaryBlocks(t *testing.T) {
	old := oldBinDir(t)
	r := newShimRepo(t, true)
	out, err := r.commit(t, r.withPath(old), "old binary")
	require.Error(t, err, out)
	assert.Contains(t, out, "unknown command")
	assert.Contains(t, out, "upgrade ethos: make install")

	require.NoError(t, os.Remove(filepath.Join(r.dir, ".git", "hooks", "pre-commit")))
	out, err = r.commit(t, r.withPath(old), "old binary, commit-msg only")
	require.Error(t, err, out)
	assert.Contains(t, out, "commit-msg")
	assert.Contains(t, out, "upgrade ethos: make install")
}

// A section copied before the shim keeps working with the new binary, and a
// refresh rewrites it to the shim.
func TestShimOldCopiedSectionStillWorksAndRefreshRewritesIt(t *testing.T) {
	r := newShimRepo(t, true)
	hooksDir := filepath.Join(r.dir, ".git", "hooks")

	// Replace both sections with the pre-shim text from oldBinaryRev.
	for name, file := range map[string]string{"pre-commit": "pre-commit.sh", "commit-msg": "commit-msg.sh"} {
		show := testhelpers.GitCommand("-C", mustAbs(t, "../.."), "show", oldBinaryRev+":plugin/hooks/"+file)
		oldText, err := show.Output()
		if err != nil {
			t.Skipf("no %s in this clone: %v", oldBinaryRev, err)
		}
		section := fmt.Sprintf("#!/bin/sh\n# --- BEGIN %s ---\n%s# --- END %s ---\n", tagFor(name), stripShebang(string(oldText)), tagFor(name))
		require.NoError(t, os.WriteFile(filepath.Join(hooksDir, name), []byte(section), 0o755))
	}
	before, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	require.NoError(t, err)
	require.Contains(t, string(before), "audit seal --commit-context", "fixture is the pre-shim section")

	env := append(r.withPath(ethosBinDir(t)), "MISSION_ID=m-2026-11-04-043")
	out, err := r.commit(t, env, "old section")
	require.NoError(t, err, out)
	assert.Contains(t, r.lastMessage(t), "Mission: m-2026-11-04-043", "the old commit-msg section still adds trailers")

	cmd := exec.Command(filepath.Join(ethosBinDir(t), "ethos"), "enable")
	cmd.Dir, cmd.Env = r.dir, r.withPath(ethosBinDir(t))
	enableOut, err := cmd.CombinedOutput()
	require.NoError(t, err, string(enableOut))
	after, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	require.NoError(t, err)
	assert.Contains(t, string(after), "hook pre-commit", "refresh rewrote the section to the shim")
	assert.NotContains(t, string(after), "audit seal --commit-context")
	trailer, err := os.ReadFile(filepath.Join(hooksDir, "commit-msg"))
	require.NoError(t, err)
	assert.Contains(t, string(trailer), "hook commit-msg")
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	require.NoError(t, err)
	return abs
}

func tagFor(hook string) string {
	if hook == "pre-commit" {
		return SealTag
	}
	return TrailerTag
}

// stripShebang drops the leading #!/bin/sh line, which the chained section sits
// below, and returns the body ending in a newline.
func stripShebang(s string) string {
	_, rest, _ := strings.Cut(s, "\n")
	if !strings.HasSuffix(rest, "\n") {
		rest += "\n"
	}
	return rest
}
