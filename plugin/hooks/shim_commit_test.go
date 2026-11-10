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

// preHelpRev is the last release whose `hook` group had no RunE and no Args
// (before #415): it answers an unknown subcommand with the group help on stdout
// and exit 0, so a shim that trusted the exit status alone would pass unsealed.
const preHelpRev = "v4.8.0"

// builtBinary is one old binary, built once per test binary.
type builtBinary struct {
	once sync.Once
	dir  string
	err  error
}

var (
	oldBuilds   = map[string]*builtBinary{oldBinaryRev: {}, preHelpRev: {}}
	oldBuildsMu sync.Mutex
)

// oldBinDir builds the ethos binary at rev once and returns its directory. The
// source is exported with git archive into a scratch directory, so the checkout
// under development is never touched.
func oldBinDir(t *testing.T, rev string) string {
	t.Helper()
	oldBuildsMu.Lock()
	b := oldBuilds[rev]
	oldBuildsMu.Unlock()
	require.NotNil(t, b, "unknown old revision %s", rev)
	b.once.Do(func() { b.dir, b.err = buildAt(rev) })
	if b.err != nil {
		missingHistory(t, "cannot build the binary at "+rev, b.err)
	}
	return b.dir
}

// missingHistory reports that the old revisions are unavailable. Locally that
// is a skip, since a shallow clone has no such commit. In CI the test job
// fetches full history, so it is a failure: these cases carry the fail-closed
// ruling and must not quietly stop running.
func missingHistory(t *testing.T, what string, err error) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("%s: %v (CI must fetch full history)", what, err)
	}
	t.Skipf("%s (a shallow clone lacks it): %v", what, err)
}

// removeOldBuilds deletes the old binaries after the last test. They are built
// once per test binary, so no single test owns their lifetime.
func removeOldBuilds() {
	for _, b := range oldBuilds {
		if b.dir != "" {
			_ = os.RemoveAll(b.dir)
		}
	}
}

// buildAt builds ./cmd/ethos as it was at rev and returns the directory holding
// the binary.
func buildAt(rev string) (string, error) {
	root, err := filepath.Abs("../..")
	if err != nil {
		return "", err
	}
	src, err := os.MkdirTemp("", "ethos-old-src")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(src)
	tarball, err := testhelpers.GitCommand("-C", root, "archive", rev).Output()
	if err != nil {
		return "", fmt.Errorf("git archive %s: %w", rev, err)
	}
	untar := exec.Command("tar", "-x", "-C", src)
	untar.Stdin = bytes.NewReader(tarball)
	if out, err := untar.CombinedOutput(); err != nil {
		return "", fmt.Errorf("extracting %s: %w\n%s", rev, err, out)
	}
	dir, err := os.MkdirTemp("", "ethos-old-bin")
	if err != nil {
		return "", err
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "ethos"), "./cmd/ethos")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("building %s: %w\n%s", rev, err, out)
	}
	return dir, nil
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
// (fail closed) and the message names the remedy. The two builds differ in how
// they refuse: 93c9f82 rejects the unknown subcommand with a non-zero exit,
// while v4.8.0 prints the hook group's help on stdout and exits 0, which a shim
// that trusted the exit status alone would let through unsealed.
func TestShimEnabledOldBinaryBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, rev, refusal string
	}{
		{"rejects the subcommand", oldBinaryRev, "unknown command"},
		{"prints help and exits 0", preHelpRev, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := oldBinDir(t, tc.rev)
			r := newShimRepo(t, true)
			out, err := r.commit(t, r.withPath(old), "old binary")
			require.Error(t, err, "the commit must not land: %s", out)
			assert.Contains(t, out, tc.refusal)
			assert.Contains(t, out, "pre-commit")
			assert.Contains(t, out, "upgrade ethos: make install")
			assert.NotContains(t, out, "Available Commands", "the old binary's help must not be left looking like a pass")
			cmd := exec.Command("git", "-C", r.dir, "rev-parse", "--verify", "-q", "HEAD")
			cmd.Env = r.env
			require.Error(t, cmd.Run(), "no commit was created")

			require.NoError(t, os.Remove(filepath.Join(r.dir, ".git", "hooks", "pre-commit")))
			out, err = r.commit(t, r.withPath(old), "old binary, commit-msg only")
			require.Error(t, err, "the commit must not land: %s", out)
			assert.Contains(t, out, "commit-msg")
			assert.Contains(t, out, "upgrade ethos: make install")
			cmd = exec.Command("git", "-C", r.dir, "rev-parse", "--verify", "-q", "HEAD")
			cmd.Env = r.env
			require.Error(t, cmd.Run(), "no commit was created")
		})
	}
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
			missingHistory(t, "no "+oldBinaryRev+":plugin/hooks/"+file, err)
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

// containsPath reports the files under root whose bytes contain needle.
func containsPath(t *testing.T, root, needle string) []string {
	t.Helper()
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte(needle)) {
			hits = append(hits, path)
		}
		return nil
	})
	require.NoError(t, err)
	return hits
}

// A live audit line written before redaction existed is redacted in place and
// archived by the first commit: no retained-data warning, no unredacted bytes
// anywhere under the local tree, and a second commit prints nothing.
func TestShimEnabledUnredactedLiveLineIsMigratedByCommit(t *testing.T) {
	r := newShimRepo(t, true)
	p, err := layout.NewRepository(r.dir, r.dir, layout.RecordBlame)
	require.NoError(t, err)
	live := audit.Stream{Namespace: audit.SessionNS, Session: "s-old"}
	line := `{"ts":"` + audit.FormatLineTS(1000) + `","session":"s-old","tool":"Bash","tool_input":{"command":"cat /Users/alice/notes.txt"},"tool_input_hash":"0000","record":"blame"}` + "\n"
	require.NoError(t, audit.WriteLocalAtomic(p, live.LivePath(p), []byte(line)))
	require.NotEmpty(t, containsPath(t, r.dir, "/Users/alice"), "fixture holds the unredacted path")

	env := r.withPath(ethosBinDir(t))
	out, err := r.commit(t, env, "first")
	require.NoError(t, err, out)
	assert.NotContains(t, out, "retained-data warning")
	assert.Empty(t, containsPath(t, filepath.Join(r.dir, ".punt-labs", "local"), "/Users/alice"), "no file keeps the unredacted path")

	out, err = r.commit(t, env, "second")
	require.NoError(t, err, out)
	assert.NotContains(t, out, "audit seal", "the second commit has nothing to warn about")
}

// A path under .punt-labs/local/ that was force-added, or that HEAD already
// tracks, is unstaged by the pre-commit hook: the commit lands without it, the
// file stays on disk, and a tracked one is deleted from the new tree.
func TestShimEnabledUnstagesLocalPaths(t *testing.T) {
	r := newShimRepo(t, true)
	env := r.withPath(ethosBinDir(t))
	localDir := filepath.Join(r.dir, ".punt-labs", "local", "ethos")
	require.NoError(t, os.MkdirAll(localDir, 0o755))
	forced := filepath.Join(localDir, "forced.txt")
	require.NoError(t, os.WriteFile(forced, []byte("forced\n"), 0o644))
	git := func(args ...string) string {
		t.Helper()
		cmd := testhelpers.GitCommand(append([]string{"-C", r.dir}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	tree := func() string { return git("ls-tree", "-r", "--name-only", "HEAD") }

	git("add", "-f", ".punt-labs/local/ethos/forced.txt")
	out, err := r.commit(t, env, "forced add")
	require.NoError(t, err, out)
	assert.Contains(t, out, "unstaged 1 path(s) under .punt-labs/local/")
	assert.NotContains(t, tree(), ".punt-labs/local/", "the commit holds no local path")
	assert.FileExists(t, forced, "the file stays on disk")

	// A path HEAD already tracks under local/ is staged for removal. Plant one
	// in HEAD with the hook bypassed, as a repository from before the hook has.
	tracked := filepath.Join(localDir, "tracked.txt")
	require.NoError(t, os.WriteFile(tracked, []byte("tracked\n"), 0o644))
	git("add", "-f", ".punt-labs/local/ethos/tracked.txt")
	git("-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", "plant")
	require.Contains(t, tree(), ".punt-labs/local/ethos/tracked.txt")

	out, err = r.commit(t, env, "remove tracked")
	require.NoError(t, err, out)
	assert.Contains(t, out, "unstaged 1 path(s) under .punt-labs/local/")
	assert.NotContains(t, tree(), ".punt-labs/local/", "the tracked local path is deleted from the tree")
	assert.FileExists(t, tracked, "the file stays on disk")

	out, err = r.commit(t, env, "nothing to unstage")
	require.NoError(t, err, out)
	assert.NotContains(t, out, "unstaged", "a commit with no local path prints nothing")
}

// A trace chunk HEAD tracks under the record tree (a v3.12-v4.20 layout) is
// removed from the tree by the next commit, with no stderr line, and stays on
// disk.
func TestShimEnabledUntracksRecordTreeTrace(t *testing.T) {
	r := newShimRepo(t, true)
	env := r.withPath(ethosBinDir(t))
	chunk := filepath.Join(r.dir, ".punt-labs", "ethos", "sessions", "2026-11-09-sid", "audit-1-2.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(chunk), 0o755))
	require.NoError(t, os.WriteFile(chunk, []byte(`{"record":"blame"}`+"\n"), 0o644))
	rel := ".punt-labs/ethos/sessions/2026-11-09-sid/audit-1-2.jsonl"
	git := func(args ...string) string {
		t.Helper()
		cmd := testhelpers.GitCommand(append([]string{"-C", r.dir}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	git("add", "-f", rel)
	git("-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", "plant")
	require.Contains(t, git("ls-tree", "-r", "--name-only", "HEAD"), rel)

	out, err := r.commit(t, env, "next")
	require.NoError(t, err, out)
	assert.NotContains(t, git("ls-tree", "-r", "--name-only", "HEAD"), rel, "the trace chunk leaves the tree")
	assert.FileExists(t, chunk, "the file stays on disk")
	assert.NotContains(t, out, "unstaged", "record-tree trace prints no line")
}
