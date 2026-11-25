//go:build linux || darwin

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/require"
)

func developmentHookFixture(t *testing.T) (dir string, run func(string, string, ...string) ([]byte, error)) {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	bin := filepath.Join(dir, ".test-bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test", "PUNT_TELEMETRY=off"}
	goEnv, err := exec.CommandContext(t.Context(), "go", "env", "GOCACHE", "GOMODCACHE").Output()
	require.NoError(t, err)
	cache, modules, ok := strings.Cut(strings.TrimSuffix(string(goEnv), "\n"), "\n")
	require.True(t, ok)
	env = append(env, "GOCACHE="+cache, "GOMODCACHE="+modules)
	run = func(at, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), name, args...)
		cmd.Dir, cmd.Env = at, testhelpers.CoverEnv(env)
		return cmd.CombinedOutput()
	}
	out, err := run(dir, "git", "init", "-q", "--template=")
	require.NoError(t, err, "%s", out)
	for _, name := range []string{"scripts", "go.mod", "docs", ".githooks"} {
		require.NoError(t, os.Symlink(filepath.Join(root, name), filepath.Join(dir, name)))
	}
	return dir, run
}

func installDevelopmentHooks(t *testing.T, dir string, run func(string, string, ...string) ([]byte, error)) {
	t.Helper()
	makefile, err := filepath.Abs("../Makefile")
	require.NoError(t, err)
	out, err := run(dir, "make", "-f", makefile, "hooks")
	require.NoError(t, err, "%s", out)
}

func writePushedHookScripts(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "scripts"), 0o700))
	for _, name := range []string{"install-dev-hooks.py", "test-timed.sh", "test-snapshot.sh"} {
		path := filepath.Join("scripts", name)
		data, err := os.ReadFile(filepath.Join("..", path))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, path), data, 0o600))
	}
}

func TestDevelopmentPrePushTimesSnapshotCleanup(t *testing.T) {
	dir, run := developmentHookFixture(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "scripts")))
	writePushedHookScripts(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-push:\n\t@echo ran-tests\n"), 0o600))
	out, err := run(dir, "git", "add", "Makefile", "scripts")
	require.NoError(t, err, "%s", out)
	out, err = run(dir, "git", "commit", "-qm", "fixture")
	require.NoError(t, err, "%s", out)
	require.NoError(t, os.Remove(filepath.Join(dir, "scripts", "test-timed.sh")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "test-snapshot.sh"), []byte("exit 99\n"), 0o600))
	out, err = run(dir, "git", "rev-parse", "HEAD")
	require.NoError(t, err, "%s", out)
	head := strings.TrimSpace(string(out))

	git, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := filepath.Join(dir, ".test-bin")
	require.NoError(t, os.Mkdir(filepath.Join(bin, "real"), 0o700))
	require.NoError(t, os.Symlink(git, filepath.Join(bin, "real", "git")))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(`#!/bin/sh
set -e
"${0%/*}/real/git" "$@"
if [ "$3" = worktree ] && [ "$4" = remove ]; then
    echo removed-snapshot
fi
`), 0o700))

	ref := "HEAD " + head + " refs/heads/topic " + strings.Repeat("0", len(head))
	out, err = run(dir, "bash", "-c", `printf '%s\n' "$1" | .githooks/pre-push`, "hook", ref)
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "ran-tests\nremoved-snapshot\npre-push:")
}

func TestDevelopmentTestPushTiming(t *testing.T) {
	dir, run := developmentHookFixture(t)
	makefile, err := filepath.Abs("../Makefile")
	require.NoError(t, err)
	args := []string{"-f", makefile, "test-push", "MAKE=echo"}
	out, err := run(dir, "make", args...)
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "\npre-push:")

	out, err = run(dir, "make", append(args, "ETHOS_TEST_PUSH_SNAPSHOT=1")...)
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "\ntest test-subprocess-fast\n")
	require.NotContains(t, string(out), "\npre-push:")
}

func TestDevelopmentHooksFutureWorktreeKeepsExistingHooks(t *testing.T) {
	dir, run := developmentHookFixture(t)
	out, err := run(dir, "git", "commit", "--allow-empty", "-m", "initial")
	require.NoError(t, err, "%s", out)
	hooks := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.Mkdir(hooks, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\necho 'existing pre-commit ran'\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "commit-msg"), []byte("#!/bin/sh\necho 'existing commit-msg ran'\n"), 0o700))
	installDevelopmentHooks(t, dir, run)
	linked := t.TempDir()
	out, err = run(dir, "git", "worktree", "add", "--detach", linked, "HEAD")
	require.NoError(t, err, "%s", out)
	out, err = run(linked, "git", "commit", "--allow-empty", "-m", "linked worktree")
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "existing pre-commit ran")
	require.Contains(t, string(out), "existing commit-msg ran")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test-changed:\n\t@echo 'development gate ran'\nsecret-scan-staged:\n\t@true\n"), 0o600))
	out, err = run(dir, "git", "add", ".githooks", "Makefile")
	require.NoError(t, err, "%s", out)
	out, err = run(dir, "git", "commit", "-m", "checkout with development gates")
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "development gate ran")

	withGates := t.TempDir()
	out, err = run(dir, "git", "worktree", "add", "--detach", withGates, "HEAD")
	require.NoError(t, err, "%s", out)
	require.FileExists(t, filepath.Join(withGates, ".githooks", "pre-commit"))
	out, err = run(withGates, "git", "commit", "--allow-empty", "-m", "sibling with gates")
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "existing pre-commit ran")
	require.Contains(t, string(out), "existing commit-msg ran")
	require.NotContains(t, string(out), "development gate ran")
}

func buildFixtureEthos(t *testing.T, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, ".test-bin", "ethos")
	build := exec.CommandContext(t.Context(), "go", testhelpers.CoverBuild("-o", binary, "./cmd/ethos")...)
	build.Dir = ".."
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return binary
}

func TestDevelopmentHooksDoctorPassesAfterOptIn(t *testing.T) {
	dir, run := developmentHookFixture(t)
	binary := buildFixtureEthos(t, dir)
	out, err := run(dir, binary, "enable")
	require.NoError(t, err, "%s", out)
	installDevelopmentHooks(t, dir, run)
	out, err = run(dir, binary, "doctor")
	require.NoError(t, err, "%s", out)
	for _, label := range []string{"Audit seal hook", "Audit trailer hook", "Seal hook currency", "Trailer hook currency"} {
		require.Regexp(t, label+`[ ]+PASS`, string(out))
	}
}

func TestDevelopmentHooksRequireLegacySealUpgrade(t *testing.T) {
	dir, run := developmentHookFixture(t)
	hooks := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.Mkdir(hooks, 0o700))
	hook := filepath.Join(hooks, "pre-commit")
	legacy := []byte("#!/bin/sh\n# hooks/pre-commit.sh — Seal pending live audit lines\nexit 71\n")
	require.NoError(t, os.WriteFile(hook, legacy, 0o700))
	makefile, err := filepath.Abs("../Makefile")
	require.NoError(t, err)
	out, err := run(dir, "make", "-f", makefile, "hooks")
	require.Error(t, err, "%s", out)
	require.Contains(t, string(out), "run ethos enable before opting in")
	got, err := os.ReadFile(hook)
	require.NoError(t, err)
	require.Equal(t, legacy, got)
	require.NoFileExists(t, filepath.Join(dir, ".git", "ethos-dev-hooks"))
	binary := buildFixtureEthos(t, dir)
	out, err = run(dir, binary, "enable")
	require.NoError(t, err, "%s", out)
	installDevelopmentHooks(t, dir, run)
	got, err = os.ReadFile(hook)
	require.NoError(t, err)
	require.Contains(t, string(got), "BEGIN Ethos development gate")
	require.Contains(t, string(got), "BEGIN ETHOS DES-058 SEAL")
	require.NotContains(t, string(got), "exit 71")
}

func TestDevelopmentHooksAcceptFencedSealWithLegacyHeader(t *testing.T) {
	dir, run := developmentHookFixture(t)
	binary := buildFixtureEthos(t, dir)
	out, err := run(dir, binary, "enable")
	require.NoError(t, err, "%s", out)
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	fenced, err := os.ReadFile(hook)
	require.NoError(t, err)
	_, rest, ok := strings.Cut(string(fenced), "\n")
	require.True(t, ok)
	const host = "# hooks/pre-commit.sh — Seal pending live audit lines\necho 'custom hook ran'\n"
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\n"+host+rest), 0o700))
	out, err = run(dir, binary, "enable")
	require.NoError(t, err, "%s", out)
	refreshed, err := os.ReadFile(hook)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(refreshed), "#!/bin/sh\n"+host))
	installDevelopmentHooks(t, dir, run)
	got, err := os.ReadFile(hook)
	require.NoError(t, err)
	require.Contains(t, string(got), "BEGIN Ethos development gate")
	require.Contains(t, string(got), host+rest)
}
