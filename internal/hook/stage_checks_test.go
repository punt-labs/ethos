//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/require"
)

func stageGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func TestStageFileSelection(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	for _, path := range []string{"-$(x) one.go", "sub/two.go", "unstaged.go", ".tmp/skip.go"} {
		writeFile(t, filepath.Join(root, path), "")
	}
	stageGit(t, root, "add", "--", "-$(x) one.go", "sub/two.go", ".tmp/skip.go")
	files, err := stageFiles(root, "commit")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"-$(x) one.go", "sub/two.go", ".tmp/skip.go"}, files)
	set, err := checkset.Decode([]byte("name: test\nchecks:\n  - name: probe\n    stage: commit\n    files: ['**/*.go']\n    run: [probe, '{files}', '{package_dir}', '{root}']\n"))
	require.NoError(t, err)
	invocations := stageInvocations(root, files, set.Checks[0])
	require.Len(t, invocations, 1)
	require.Equal(t, []string{"probe", filepath.Join(root, "-$(x) one.go"), filepath.Join(root, "sub/two.go"), "./", "./sub", root}, invocations[0].argv)
	require.Equal(t, 120*time.Second, invocations[0].timeout)
	require.Empty(t, stageInvocations(root, nil, set.Checks[0]))
	set.Checks[0].Files = checkset.Patterns{All: true}
	require.Len(t, stageInvocations(root, nil, set.Checks[0]), 1)
	set.Checks[0].Cwd = "package_dir"
	invocations = stageInvocations(root, files, set.Checks[0])
	require.Len(t, invocations, 2)
	require.Equal(t, filepath.Join(root, "sub"), invocations[1].cwd)
	handoff, err := stageFiles(root, "handoff")
	require.NoError(t, err)
	require.Contains(t, handoff, "unstaged.go")
	index := filepath.Join(t.TempDir(), "index")
	t.Setenv("GIT_INDEX_FILE", index)
	stageGit(t, root, "read-tree", "HEAD")
	stageGit(t, root, "add", "unstaged.go")
	files, err = stageFiles(root, "commit")
	require.NoError(t, err)
	require.Equal(t, []string{"unstaged.go"}, files)
}

func TestHandoffChecks(t *testing.T) {
	for _, tc := range []struct {
		name, mode             string
		active, killed, broken bool
	}{
		{name: "block"}, {name: "advisory", mode: "    on_fail: advisory\n"},
		{name: "reentry", active: true}, {name: "kill", killed: true},
		{name: "broken", broken: true}, {name: "broken reentry", broken: true, active: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := postEditFixture(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			checkPolicy(t, root, "project", "  - name: handoff-probe\n    stage: handoff\n    files: all\n    run: [probe]\n"+tc.mode)
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			postEditTool(t, bin, "probe", "echo failure; exit 1\n")
			if tc.broken {
				writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: absent\n")
			}
			if tc.killed {
				writeFile(t, filepath.Join(home, ".punt-hooks-kill"), "")
			}
			ss := session.NewStore(t.TempDir())
			require.NoError(t, ss.Create("stop", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub", Persona: "worker"}, "", ""))
			payload, err := json.Marshal(map[string]any{"session_id": "stop", "agent_id": "sub", "agent_type": "worker", "stop_hook_active": tc.active})
			require.NoError(t, err)
			var out, stderr bytes.Buffer
			require.NoError(t, HandleSubagentStopWithIO(context.Background(), bytes.NewReader(payload), ss, &out, &stderr))
			roster, err := ss.Load("stop")
			require.NoError(t, err)
			require.Nil(t, roster.FindParticipant("sub"))
			if tc.killed {
				require.Empty(t, out.String())
				require.Empty(t, stderr.String())
				return
			}
			if tc.active || tc.mode != "" {
				require.Empty(t, out.String())
				require.NotEmpty(t, stderr.String())
			} else {
				require.Contains(t, out.String(), `"decision":"block"`)
				require.True(t, strings.Contains(out.String(), "handoff-probe") || strings.Contains(out.String(), "absent"))
			}
		})
	}
}

func TestHandoffAuditFiles(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, "written.go"), "")
	writeFile(t, filepath.Join(root, "other.go"), "")
	writeRepoTreeSession(t, root, "2026-11-21", "audit-stop", []auditEntry{
		{Ts: "2026-11-21T10:00:00Z", Session: "audit-stop", AgentID: "sub", Tool: "Write", ToolInput: map[string]any{"file_path": "<repo>/written.go"}, PathFormat: auditPathFormat},
		{Ts: "2026-11-21T10:00:01Z", Session: "audit-stop", AgentID: "other", Tool: "Edit", ToolInput: map[string]any{"file_path": "<repo>/other.go"}, PathFormat: auditPathFormat},
	})
	files, err := handoffFiles(root, "audit-stop", "sub")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{root: {"written.go"}}, files)
	files, err = handoffFiles(root, "missing", "sub")
	require.NoError(t, err)
	require.Contains(t, files[root], "other.go")
}

func TestStageRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, stage, fields, script string
		wantBlock, wantAdvice       bool
	}{
		{"success", "commit", "", "exit 0\n", false, false},
		{"output", "commit", "    fail_on_output: true\n", "echo output\n", true, false},
		{"advisory", "commit", "    on_fail: advisory\n", "echo failed; exit 1\n", false, true},
		{"timeout", "commit", "    timeout: 5ms\n", "while :; do :; done\n", true, false},
		{"handoff", "handoff", "", "echo failed; exit 1\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("GIT_TRACE_STAGE", "must-not-leak")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, root, "project", "  - name: runtime\n    stage: "+tc.stage+"\n    files: all\n    run: [probe]\n"+tc.fields)
			postEditTool(t, bin, "probe", "[ -z \"$GIT_TRACE_STAGE\" ] || { echo leaked; exit 2; }\n"+tc.script)
			feedback := RunStageChecks(context.Background(), root, "", tc.stage)
			require.Equal(t, tc.wantBlock, feedback.Blocking != "")
			require.Equal(t, tc.wantAdvice, feedback.Advisory != "")
			if tc.name == "timeout" {
				require.Contains(t, feedback.Blocking, "timed out")
			}
			writeFile(t, filepath.Join(os.Getenv("HOME"), ".punt-hooks-kill"), "")
			require.Equal(t, StageFeedback{}, RunStageChecks(context.Background(), root, "", tc.stage))
		})
	}
}

func TestStageEmptyExecutable(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: empty-command\n    stage: handoff\n    files: all\n    run: ['{files}']\n")
	feedback := runStageChecks(context.Background(), root, "", "handoff", func() ([]string, error) { return nil, nil })
	require.Contains(t, feedback.Blocking, "empty-command")
}

func TestCommitCheckIndexEnvironment(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	index := filepath.Join(t.TempDir(), "index")
	t.Setenv("GIT_INDEX_FILE", index)
	log := filepath.Join(t.TempDir(), "index.log")
	t.Setenv("STAGE_LOG", log)
	postEditTool(t, bin, "probe", "printf '%s' \"$GIT_INDEX_FILE\" > \"$STAGE_LOG\"\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: index\n    stage: commit\n    files: all\n    run: [probe]\n")
	require.Equal(t, StageFeedback{}, RunStageChecks(context.Background(), root, "", "commit"))
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, index, string(data))
}

func TestStagePolicyErrorsAndOptOut(t *testing.T) {
	for _, tc := range []struct {
		ref    string
		blocks bool
	}{{"", false}, {"none", false}, {"absent", true}, {"Go_Default", true}} {
		t.Run(tc.ref, func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			if tc.ref != "" {
				writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: "+tc.ref+"\n")
			}
			feedback := RunStageChecks(context.Background(), root, "", "commit")
			require.Equal(t, tc.blocks, feedback.Blocking != "")
			if tc.blocks {
				require.Contains(t, feedback.Blocking, tc.ref)
			}
		})
	}
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	require.Contains(t, RunStageChecks(context.Background(), root, "", "unknown").Blocking, "unsupported stage")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: files\n    stage: commit\n    files: all\n    run: [missing]\n")
	t.Setenv("GIT_INDEX_FILE", "")
	require.Contains(t, RunStageChecks(context.Background(), root, "", "commit").Blocking, "GIT_INDEX_FILE")
}

func TestStageFailureTailAndDeadline(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: tail\n    stage: commit\n    files: all\n    run: [probe]\n")
	postEditTool(t, bin, "probe", "i=0; while [ $i -lt 60 ]; do printf 'line-%02d\\n' \"$i\"; i=$((i+1)); done; exit 1\n")
	feedback := RunStageChecks(context.Background(), root, "", "commit")
	require.Contains(t, feedback.Blocking, "line-59")
	require.NotContains(t, feedback.Blocking, "line-00")
	postEditTool(t, bin, "probe", "while :; do :; done\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	feedback = RunStageChecks(ctx, root, "", "commit")
	require.Contains(t, feedback.Blocking, "timed out")
}

func TestHandoffLinkedWorktree(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint("fallback=", fallback), func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			linked := filepath.Join(root, ".claude", "worktrees", "linked")
			stageGit(t, root, "worktree", "add", "--detach", linked)
			writeFile(t, filepath.Join(linked, "written.go"), "package linked\n")
			writeFile(t, filepath.Join(linked, ".punt-labs/ethos.yaml"), "checks: none\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, root, "project", "  - name: glob\n    stage: handoff\n    files: ['**/*.go']\n    run: [probe, '{root}', '{files}']\n  - name: all\n    stage: handoff\n    files: all\n    run: [probe, '{root}']\n")
			log := filepath.Join(t.TempDir(), "log")
			t.Setenv("STAGE_LOG", log)
			postEditTool(t, bin, "probe", "printf '%s|' \"$PWD\" >> \"$STAGE_LOG\"; printf '%s|' \"$@\" >> \"$STAGE_LOG\"; printf '\\n' >> \"$STAGE_LOG\"\n")
			path := "<repo>/.claude/worktrees/linked/written.go"
			if fallback {
				path = "<repo>/.claude/worktrees/linked/removed.go"
			}
			writeRepoTreeSession(t, root, "2026-11-21", "linked-stop", []auditEntry{{Ts: "2026-11-21T10:00:00Z", Session: "linked-stop", AgentID: "sub", Tool: "Write", ToolInput: map[string]any{"file_path": path}, PathFormat: auditPathFormat}})
			ss := session.NewStore(t.TempDir())
			require.NoError(t, ss.Create("linked-stop", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
			var out, stderr bytes.Buffer
			require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"linked-stop","agent_id":"sub"}`), ss, &out, &stderr))
			require.Empty(t, out.String())
			require.Empty(t, stderr.String())
			data, err := os.ReadFile(log)
			require.NoError(t, err)
			require.Equal(t, linked+"|"+linked+"|"+filepath.Join(linked, "written.go")+"|\n"+linked+"|"+linked+"|\n", string(data))
		})
	}
}

func TestHandoffWithoutGit(t *testing.T) {
	for _, stage := range []string{"", "edit", "handoff"} {
		t.Run(stage, func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			if stage != "" {
				writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
				files := "all"
				if stage == "edit" {
					files = "['**/*.go']"
				}
				checkPolicy(t, root, "project", "  - name: probe\n    stage: "+stage+"\n    files: "+files+"\n    run: [missing]\n")
			}
			t.Setenv("PATH", t.TempDir())
			ss := session.NewStore(t.TempDir())
			require.NoError(t, ss.Create("no-git", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
			var out, stderr bytes.Buffer
			require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"no-git","agent_id":"sub"}`), ss, &out, &stderr))
			roster, err := ss.Load("no-git")
			require.NoError(t, err)
			require.Nil(t, roster.FindParticipant("sub"))
			if stage == "handoff" {
				require.Contains(t, out.String(), `"decision":"block"`)
			} else {
				require.Empty(t, out.String())
				require.Empty(t, stderr.String())
			}
			require.Equal(t, StageFeedback{}, RunStageChecks(context.Background(), root, "", "commit"))
		})
	}
}
