package hook

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDarwinLiveSentinelCutoff(t *testing.T) {
	for _, mutate := range []bool{false, true} {
		root := t.TempDir()
		root, err := filepath.EvalSymlinks(root)
		require.NoError(t, err)
		initGitRepo(t, root)
		writeFile(t, filepath.Join(root, "input"), "clean")
		stageGit(t, root, "add", "input")
		guard, err := armHandoffLive(context.Background(), root, []string{"input"})
		require.NoError(t, err)
		defer guard.close()
		if mutate {
			require.NoError(t, os.WriteFile(filepath.Join(root, "input"), []byte("dirty"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(root, "input"), []byte("clean"), 0o600))
			require.ErrorContains(t, guard.(*darwinHandoffLive).events.finish(context.Background(), []string{"input"}), "FSEvents")
		} else {
			require.NoError(t, guard.finish(context.Background(), []string{"input"}))
		}
	}
}

func TestDarwinLiveLinkedWorktree(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root := filepath.Join(parent, "linked")
	main := filepath.Join(parent, "main")
	require.NoError(t, os.Mkdir(main, 0o700))
	initGitRepo(t, main)
	writeFile(t, filepath.Join(main, "input"), "clean")
	stageGit(t, main, "add", "input")
	stageGit(t, main, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qm", "input")
	stageGit(t, main, "worktree", "add", "--detach", root)
	stageGit(t, main, "pack-refs", "--all")

	for _, change := range []string{"", "input", "HEAD", "index", "refs/heads/transient", "packed-refs"} {
		t.Run(change, func(t *testing.T) {
			guard, err := armHandoffLive(t.Context(), root, []string{"input"})
			require.NoError(t, err)
			defer guard.close()
			gitDir := filepath.Join(main, ".git", "worktrees", "linked")
			require.Equal(t, filepath.Join(gitDir, "ethos-live"), filepath.Dir(guard.(*darwinHandoffLive).events.dir))
			if change == "" {
				require.NoError(t, guard.finish(t.Context(), []string{"input"}))
				return
			}
			dir := gitDir
			if change == "input" {
				dir = root
			} else if change == "packed-refs" || change == "refs/heads/transient" {
				dir = filepath.Join(main, ".git")
			}
			path := filepath.Join(dir, change)
			original, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				require.NoError(t, os.WriteFile(path, []byte("changed"), 0o600))
				require.NoError(t, os.Remove(path))
			} else {
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, []byte("changed"), 0o600))
				require.NoError(t, os.WriteFile(path, original, 0o600))
			}
			require.ErrorContains(t, guard.(*darwinHandoffLive).events.finish(t.Context(), []string{"input"}), "FSEvents observed a change")
			require.ErrorContains(t, guard.(*darwinHandoffLive).vnodes.drain(), "vnode")
		})
	}
}

func TestDarwinLiveGateApprovalAndVerdict(t *testing.T) {
	root, _ := postEditFixture(t)
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".gitignore"), ".punt-labs/local/\noutput/\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - {name: live, stage: handoff, files: all, run: [/bin/sh, gate.sh]}\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"),
		"checks:\n  live:\n    profile: linux-worktree-v1\n    isolation: live-verified\n    epoch: proof\n    sources: {mode: worktree}\n")
	writeFile(t, filepath.Join(root, "input"), "pass\n")
	writeFile(t, filepath.Join(root, "gate.sh"), "echo ran >> output/launches; read status < input; test \"$status\" = pass\n")
	launches := filepath.Join(root, "output/launches")
	require.NoError(t, os.Mkdir(filepath.Dir(launches), 0o700))
	stageGit(t, root, "add", ".")
	policy, _, err := configuredStageChecks(root, "", "handoff")
	require.NoError(t, err)
	require.Len(t, policy, 1)
	run := func() StageFeedback { return runHandoffGate(context.Background(), root, "", nil, policy[0], policy) }
	require.Contains(t, run().Advisory, "live-verified not approved (run: ethos handoff approve-live project)")
	require.NoFileExists(t, launches)
	approval, err := ResolveHandoffLivePolicy(root, "project")
	require.NoError(t, err)
	require.NoError(t, approval.Approve())
	require.Empty(t, run())
	require.Empty(t, run())
	content, err := os.ReadFile(launches)
	require.NoError(t, err)
	require.Equal(t, "ran\n", string(content))
	require.NoError(t, approval.Revoke())
	require.Contains(t, run().Advisory, "live-verified not approved")
	require.NoError(t, approval.Approve())
	require.Empty(t, run())
	content, err = os.ReadFile(launches)
	require.NoError(t, err)
	require.Equal(t, "ran\nran\n", string(content))
	writeFile(t, filepath.Join(root, "input"), "fail\n")
	require.Contains(t, run().Blocking, "exit status 1")
}
