package hook

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/stretchr/testify/require"
)

func TestHandoffGitUnbornSHA256AndSplitIndex(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) {
		t.Helper()
		_, err := gitenv.Output(dir, "git", args...)
		require.NoError(t, err)
	}
	git("init", "--object-format=sha256", "--initial-branch=unborn")
	profile := &checkset.WorktreeSnapshot{Git: &checkset.SnapshotGit{}}
	view, err := captureHandoffGit(ctx, dir, profile)
	require.NoError(t, err)
	require.Equal(t, "ref: refs/heads/unborn\n", string(view.files["HEAD"]))
	require.Contains(t, string(view.files["config"]), "objectformat = sha256")
	require.Empty(t, view.objects)
	dest := filepath.Join(t.TempDir(), "git")
	require.NoError(t, view.materialize(ctx, dir, dest))
	require.NoFileExists(t, filepath.Join(dest, "refs/heads/unborn"))
	profile.Git.Refs = []string{"refs/heads/missing"}
	_, err = captureHandoffGit(ctx, dir, profile)
	require.ErrorContains(t, err, "git view ref refs/heads/missing is missing")

	profile.Git.Refs = nil
	writeFile(t, filepath.Join(dir, "staged"), "indexed content")
	git("add", "staged")
	git("update-index", "--split-index")
	view, err = captureHandoffGit(ctx, dir, profile)
	require.NoError(t, err)
	require.Contains(t, view.files, "index")
	var sharedIndexes []string
	for file := range view.files {
		if strings.HasPrefix(file, "sharedindex.") {
			sharedIndexes = append(sharedIndexes, file)
		}
	}
	require.Len(t, sharedIndexes, 1)
	require.Len(t, strings.TrimSpace(string(view.objects)), 64)
	require.NoError(t, view.materialize(ctx, dir, dest))
	require.FileExists(t, filepath.Join(dest, sharedIndexes[0]))
}

func TestHandoffGitViewRejectsMalformedSelectedRefs(t *testing.T) {
	oid := strings.Repeat("a", 40)
	const name = "refs/remotes/origin/HEAD"
	profile := &checkset.SnapshotGit{Refs: []string{name}}
	for _, tc := range []struct{ line, head, reason string }{
		{"invalid line", oid, "invalid Git view ref"},
		{oid + " " + name + " ../escape ", oid, "invalid Git view symbolic ref"},
		{oid + " " + name + "  ", "ref: ../escape", "invalid Git view HEAD"},
	} {
		view := handoffGitView{files: map[string][]byte{"HEAD": []byte(tc.head + "\n")}}
		_, err := view.addRefs([]byte(tc.line+"\n"), profile, len(oid))
		require.ErrorContains(t, err, tc.reason)
	}
}

func TestHandoffGitViewRefusesMissingMetadataAndObjects(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	profile := &checkset.WorktreeSnapshot{Git: &checkset.SnapshotGit{}}
	_, err := captureHandoffGit(ctx, root, profile)
	require.ErrorContains(t, err, "cannot capture Git view HEAD")
	_, err = readHandoffGitFile(ctx, filepath.Join(root, "missing", "HEAD"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Mkdir(filepath.Join(root, "HEAD"), 0o700))
	_, err = readHandoffGitFile(ctx, filepath.Join(root, "HEAD"))
	require.ErrorContains(t, err, "not a regular file")
	_, err = gitenv.Output(root, "git", "init", "--object-format=sha1")
	require.NoError(t, err)
	_, err = gitenv.Output(root, "git", "update-index", "--add", "--cacheinfo", "100644,"+strings.Repeat("a", 40)+",missing")
	require.NoError(t, err)
	_, err = captureHandoffGit(ctx, root, profile)
	require.ErrorContains(t, err, "cannot enumerate Git view objects")
}

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

func TestHandoffWorktreeGitDiscoveryAcrossScratchOverlay(t *testing.T) {
	root, run := handoffMetadataFixture(t, "test \"$(git -C source rev-parse --git-dir)\" = /checkout/.git")
	writeFile(t, filepath.Join(root, "source", "input"), "snapshot input\n")
	stageGit(t, root, "add", "source/input")
	sidecar := filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml")
	data, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	profile := string(data) + "    git: {}\n    scratch: {files: [output.tmp]}\n"
	writeFile(t, sidecar, profile)
	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)

	writeFile(t, sidecar, profile+"    env: {GIT_DISCOVERY_ACROSS_FILESYSTEM: \"0\"}\n"+
		"    prehydrate: {run: [git, -C, source, rev-parse, --git-dir]}\n")
	require.Empty(t, run().Blocking)
}

func TestHandoffWorktreeOmitsUndeclaredGitView(t *testing.T) {
	_, run := handoffMetadataFixture(t, "test ! -e .git")
	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
}
