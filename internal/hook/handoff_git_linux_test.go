package hook

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/stretchr/testify/require"
)

func TestHandoffGitViewIgnoresUnselectedRefsAndObjects(t *testing.T) {
	root, _ := handoffMetadataFixture(t, `set -eu
if git show-ref --verify --quiet refs/heads/sibling; then exit 41; fi
if git show-ref --verify --quiet refs/remotes/origin/unrelated; then exit 42; fi
if git cat-file -e "$(cat hidden-oid)"; then exit 43; fi
git show-ref --verify refs/heads/selected
git show-ref --verify refs/remotes/origin/selected
`)
	writeFile(t, filepath.Join(root, ".gitignore"), ".punt-labs/local/\n")
	sidecar := filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml")
	data, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	writeFile(t, sidecar, string(data)+"    git: {base_ref: refs/heads/selected}\n")
	sibling := filepath.Join(t.TempDir(), "sibling")
	git := func(dir string, args ...string) []byte {
		t.Helper()
		out, err := gitenv.Output(dir, "git", args...)
		require.NoError(t, err)
		return out
	}
	git(root, "branch", "selected")
	git(root, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	git(root, "config", "branch.selected.remote", "origin")
	git(root, "config", "branch.selected.merge", "refs/heads/selected")
	git(root, "update-ref", "refs/remotes/origin/selected", "HEAD")
	git(root, "worktree", "add", "-b", "sibling", sibling)
	commitSibling := func(text string) {
		writeFile(t, filepath.Join(sibling, "change"), text)
		git(sibling, "add", "change")
		git(sibling, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", text)
		git(root, "update-ref", "refs/remotes/origin/unrelated", "refs/heads/sibling")
	}
	commitSibling("first")
	writeFile(t, filepath.Join(root, "hidden-oid"), string(git(sibling, "rev-parse", "HEAD")))
	var logs bytes.Buffer
	ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
	ctx, _ = diagnostic.WithHookInvocation(ctx)
	run := func() StageFeedback { return RunStageChecks(ctx, root, "worker", "handoff") }

	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
	commitSibling("second")
	require.Empty(t, run().Blocking)
	require.Equal(t, 1, bytes.Count(logs.Bytes(), []byte("outcome=launched")))
	require.Contains(t, logs.String(), "outcome=reused")

	writeFile(t, sidecar, string(data)+"    git: {base_ref: refs/heads/selected, refs: [refs/heads/sibling, refs/remotes/origin/unrelated]}\n")
	require.Contains(t, run().Blocking, "exit status 41")
}

func TestHandoffWorktreeGitView(t *testing.T) {
	root, run := handoffMetadataFixture(t, `set -eu
test -d .git
branch="$(cat expected-branch)"
head="$(cat expected-head)"
if [ "$(git branch --show-current)" != "$branch" ]; then exit 44; fi
test "$(git rev-parse HEAD)" = "$head"
if [ -n "$branch" ]; then
    test "$(cat .git/HEAD)" = "ref: refs/heads/$branch"
    test "$(git rev-parse "refs/heads/$branch")" = "$head"
else
    test "$(cat .git/HEAD)" = "$head"
fi
test "$(git merge-base HEAD refs/remotes/origin/main)" = "$(git rev-parse HEAD^)"
test "$(git rev-parse refs/tags/snapshot^{} )" = "$(git rev-parse HEAD)"
test "$(git show :added)" = indexed
test "$(cat added)" = changed
test ! -e deleted
test -x executable
test "$(git show HEAD:deleted)" = original
test ! -e .git/host-only
test ! -e .git/hooks
git worktree add --detach /scratch/detached HEAD
test "$(git -C /scratch/detached rev-parse HEAD)" = "$(git rev-parse HEAD)"
test -e .git/worktrees/detached/HEAD
if (echo write > .git/HEAD); then exit 43; fi
if git show-ref --verify --quiet refs/tags/changed; then exit 42; fi
`)
	sidecar := filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml")
	data, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	writeFile(t, sidecar, string(data)+"    git: {base_ref: refs/remotes/origin/main}\n")
	git := func(args ...string) {
		t.Helper()
		_, err := gitenv.Output(root, "git", args...)
		require.NoError(t, err)
	}
	writeFile(t, filepath.Join(root, "deleted"), "original\n")
	writeFile(t, filepath.Join(root, "executable"), "original\n")
	git("add", "deleted", "executable")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "files")
	git("branch", "-m", "source-branch")
	head, err := gitenv.Output(root, "git", "rev-parse", "HEAD")
	require.NoError(t, err)
	writeFile(t, filepath.Join(root, "expected-head"), string(head))
	expectedBranch := filepath.Join(root, "expected-branch")
	writeFile(t, expectedBranch, "source-branch\n")
	git("update-ref", "refs/remotes/origin/main", "HEAD^")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "tag", "-a", "snapshot", "-m", "annotated")
	git("gc", "--quiet")
	git("rm", "deleted")
	writeFile(t, filepath.Join(root, "added"), "indexed\n")
	git("add", "added")
	writeFile(t, filepath.Join(root, "added"), "changed\n")
	require.NoError(t, os.Chmod(filepath.Join(root, "executable"), 0o755))
	writeFile(t, filepath.Join(root, ".git/host-only"), "not a snapshot input")

	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
	require.NoDirExists(t, filepath.Join(root, ".git/worktrees"))
	git("tag", "changed")
	require.Contains(t, run().Blocking, "exit status 42")
	git("tag", "-d", "changed")
	git("checkout", "--detach", "HEAD")
	require.Contains(t, run().Blocking, "exit status 44")
	writeFile(t, expectedBranch, "\n")
	require.Empty(t, run().Blocking)
}

func TestHandoffWorktreeOmitsUndeclaredGitView(t *testing.T) {
	_, run := handoffMetadataFixture(t, "test ! -e .git")
	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
}
