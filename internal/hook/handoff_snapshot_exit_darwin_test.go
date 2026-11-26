package hook

import (
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/require"
)

func TestDarwinHandoffSnapshotExit127Blocks(t *testing.T) {
	root, _ := postEditFixture(t)
	writeFile(t, filepath.Join(root, ".gitignore"), ".punt-labs/local/\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - {name: snapshot, stage: handoff, files: all, run: [/bin/sh, -c, missing-ethos-test-command]}\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), "checks:\n  snapshot:\n    profile: linux-worktree-v1\n    epoch: proof\n    sources: {mode: worktree}\n")
	ctx, _ := diagnostic.WithHookInvocation(t.Context())
	result := RunStageChecks(ctx, root, "worker", "handoff")
	require.Contains(t, result.Blocking, "exit status 127", "%+v", result)
	require.Empty(t, result.Notice, "%+v", result)
}
