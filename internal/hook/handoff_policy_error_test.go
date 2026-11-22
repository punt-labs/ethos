//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/require"
)

func TestHandoffAuditReadFailureWithoutChecks(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(liveSessionsDir(root), "probe.audit.jsonl"), 0o700))
	ss := session.NewStore(t.TempDir())
	require.NoError(t, ss.Create("probe", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
	var out, stderr bytes.Buffer
	require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"probe","agent_id":"sub"}`), ss, &out, &stderr))
	require.Empty(t, out.String())
	require.Empty(t, stderr.String())
}

func TestHandoffFileFailurePolicySelection(t *testing.T) {
	for _, tc := range []struct {
		name, stage string
		linked      bool
	}{
		{name: "unconfigured"},
		{name: "commit only", stage: "commit"},
		{name: "handoff", stage: "handoff"},
		{name: "linked unconfigured", linked: true},
		{name: "linked commit only", stage: "commit", linked: true},
		{name: "linked handoff", stage: "handoff", linked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			policyRoot := root
			if tc.linked {
				policyRoot = filepath.Join(t.TempDir(), "linked")
				stageGit(t, root, "worktree", "add", "--detach", policyRoot)
			}
			if tc.stage != "" {
				writeFile(t, filepath.Join(policyRoot, ".punt-labs/ethos.yaml"), "checks: project\n")
				checkPolicy(t, policyRoot, "project", "  - name: probe\n    stage: "+tc.stage+"\n    files: all\n    run: [probe]\n")
			}
			feedback := runStageRootChecks(context.Background(), root, "", "handoff", func() (map[string][]string, error) {
				return nil, errors.New("audit unavailable")
			})
			if tc.stage == "handoff" {
				require.Contains(t, feedback.Blocking, "audit unavailable")
			} else {
				require.Equal(t, StageFeedback{}, feedback)
			}
		})
	}
}
