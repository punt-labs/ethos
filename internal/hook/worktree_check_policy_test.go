//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorktreeCheckPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, main, local, want string
	}{
		{"worktree only", "", "branch", "branch"},
		{"main fallback", "main", "", "main"},
		{"worktree wins", "main", "branch", "branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			worktree := filepath.Join(root, ".claude/worktrees/linked")
			stageGit(t, root, "worktree", "add", "--detach", worktree)
			for dir, slug := range map[string]string{root: tc.main, worktree: tc.local} {
				writeFile(t, filepath.Join(dir, ".punt-labs/ethos.yaml"), "checks: '"+slug+"'\n")
				if slug == "" {
					continue
				}
				checks := ""
				for _, stage := range []string{"edit", "commit", "handoff"} {
					files := "all"
					if stage == "edit" {
						files = "['**']"
					}
					checks += "  - name: " + slug + "-" + stage + "\n    stage: " + stage + "\n    files: " + files + "\n    run: [probe]\n    on_fail: advisory\n"
				}
				checkPolicy(t, dir, slug, checks)
			}
			log := filepath.Join(t.TempDir(), "runs")
			t.Setenv("CHECK_POLICY_LOG", log)
			postEditTool(t, bin, "probe", "printf '%s\\n' \"$PWD\" >> \"$CHECK_POLICY_LOG\"; echo failure; exit 1\n")
			file := filepath.Join(worktree, "file.txt")
			writeFile(t, file, "edited\n")
			stageGit(t, worktree, "add", "file.txt")
			for _, invoking := range []string{worktree, root} {
				t.Setenv("CLAUDE_PROJECT_DIR", invoking)
				assert.Contains(t, configuredEdit(t, file, ""), tc.want+"-edit")
			}
			feedback := RunStageChecks(context.Background(), worktree, "", "commit")
			require.Empty(t, feedback.Blocking)
			assert.Contains(t, feedback.Advisory, tc.want+"-commit")
			writeRepoTreeSession(t, root, "2026-11-22", "policy", []auditEntry{{
				Ts: "2026-11-22T10:00:00Z", Session: "policy", AgentID: "sub", Tool: "Edit",
				ToolInput: map[string]any{"file_path": "<repo>/.claude/worktrees/linked/file.txt"}, PathFormat: auditPathFormat,
			}})
			ss := session.NewStore(t.TempDir())
			require.NoError(t, ss.Create("policy", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
			var out, stderr bytes.Buffer
			require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"policy","agent_id":"sub"}`), ss, &out, &stderr))
			require.Empty(t, out.String())
			assert.Contains(t, stderr.String(), tc.want+"-handoff")
			data, err := os.ReadFile(log)
			require.NoError(t, err)
			require.Equal(t, strings.Repeat(worktree+"\n", 4), string(data))
		})
	}
}

func TestWorktreeCheckReferenceDoesNotMixRoots(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	worktree := filepath.Join(t.TempDir(), "linked")
	stageGit(t, root, "worktree", "add", "--detach", worktree)
	for _, dir := range []string{root, worktree} {
		writeFile(t, filepath.Join(dir, ".punt-labs/ethos.yaml"), "checks: project\n")
	}
	checkPolicy(t, root, "project", "  - name: main-only\n    stage: handoff\n    files: all\n    run: [probe]\n")
	_, _, err := configuredStageChecks(worktree, "", "handoff")
	require.Error(t, err)
	writeFile(t, filepath.Join(worktree, ".punt-labs/ethos.yaml"), "checks: none\n")
	checks, selected, err := configuredStageChecks(worktree, "", "handoff")
	require.NoError(t, err)
	require.True(t, selected)
	require.Empty(t, checks)
}
