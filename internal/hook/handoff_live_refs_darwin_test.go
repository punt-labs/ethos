package hook

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/require"
)

func TestDarwinHandoffLiveTagChangeInvalidatesVerdict(t *testing.T) {
	root := approvedDarwinLiveGate(t, `[/bin/sh, -c, 'test "$(git rev-parse gate-input)" = "$(git rev-parse HEAD)"']`, "")
	stageGit(t, root, "add", ".")
	stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qm", "policy")
	stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "second")
	stageGit(t, root, "tag", "gate-input")
	ctx, _ := diagnostic.WithHookInvocation(t.Context())
	require.Equal(t, StageFeedback{}, RunStageChecks(ctx, root, "worker", "handoff"))
	stageGit(t, root, "pack-refs", "--all")
	stageGit(t, root, "tag", "-f", "gate-input", "HEAD^")
	ctx, _ = diagnostic.WithHookInvocation(t.Context())
	require.Contains(t, RunStageChecks(ctx, root, "worker", "handoff").Blocking, "exit status 1")
}

func TestDarwinHandoffLiveUnbornRefChangeInvalidatesVerdict(t *testing.T) {
	root := approvedDarwinLiveGate(t, `[/bin/sh, -c, 'test "$(git symbolic-ref refs/tags/gate-input)" = refs/heads/missing-a']`, "")
	stageGit(t, root, "symbolic-ref", "refs/tags/gate-input", "refs/heads/missing-a")
	ctx, _ := diagnostic.WithHookInvocation(t.Context())
	require.Equal(t, StageFeedback{}, RunStageChecks(ctx, root, "worker", "handoff"))
	stageGit(t, root, "symbolic-ref", "refs/tags/gate-input", "refs/heads/missing-b")
	ctx, _ = diagnostic.WithHookInvocation(t.Context())
	require.Contains(t, RunStageChecks(ctx, root, "worker", "handoff").Blocking, "exit status 1")
}
