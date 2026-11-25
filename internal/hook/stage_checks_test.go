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
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/require"
)

func stageGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func TestHandoffWithoutRepositoryOrChecks(t *testing.T) {
	for _, repository := range []bool{false, true} {
		t.Run(fmt.Sprintf("repository=%t", repository), func(t *testing.T) {
			root := testhelpers.OutsideRepo(t)
			if repository {
				initGitRepo(t, root)
			}
			t.Chdir(root)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CLAUDE_PROJECT_DIR", root)
			ss := session.NewStore(t.TempDir())
			require.NoError(t, ss.Create("plain", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
			var out, stderr bytes.Buffer
			require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"plain","agent_id":"sub"}`), ss, &out, &stderr))
			require.Empty(t, out.String())
			require.Empty(t, stderr.String())
			require.Equal(t, StageFeedback{}, RunStageChecks(context.Background(), root, "", "handoff"))
			if !repository {
				called := false
				feedback := runStageRootChecks(context.Background(), root, "", "handoff", func() (map[string][]string, error) {
					called = true
					return nil, nil
				})
				require.Equal(t, StageFeedback{}, feedback)
				require.False(t, called, "non-Git handoffs cannot have repository checks and must not discover files")
			}
		})
	}
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
			if tc.killed {
				require.NotNil(t, roster.FindParticipant("sub"))
				require.Empty(t, out.String())
				require.Empty(t, stderr.String())
				return
			}
			require.Nil(t, roster.FindParticipant("sub"))
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

func TestHandoffResultPerTree(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			checkPolicy(t, root, "project", "  - name: gate\n    stage: handoff\n    files: all\n    run: [probe]\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			count := filepath.Join(t.TempDir(), "runs")
			t.Setenv("GATE_COUNT", count)
			body := "echo gate >> \"$GATE_COUNT\"\n"
			if fail {
				body += "echo failure; exit 1\n"
			}
			postEditTool(t, bin, "probe", body)
			run := func() StageFeedback { return RunStageChecks(context.Background(), root, "", "handoff") }
			first := run()
			require.Equal(t, fail, first.Blocking != "")
			require.Equal(t, first, run())
			runs, err := os.ReadFile(count)
			require.NoError(t, err)
			require.Equal(t, "gate\n", string(runs))
			writeFile(t, filepath.Join(root, "dirty"), "one\n")
			run()
			writeFile(t, filepath.Join(root, "dirty"), "two\n")
			run()
			checkPolicy(t, root, "project", "  - name: changed-policy\n    stage: handoff\n    files: all\n    run: [probe]\n")
			run()
			runs, err = os.ReadFile(count)
			require.NoError(t, err)
			require.Equal(t, strings.Repeat("gate\n", 4), string(runs))
		})
	}
}

func TestHandoffKilledBeforeInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".punt-hooks-kill"), "")
	for _, payload := range []string{"", "{"} {
		var out, stderr bytes.Buffer
		require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(payload), nil, &out, &stderr))
		require.Empty(t, out.String())
		require.Empty(t, stderr.String())
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
	require.Empty(t, files[root])
}

func TestHandoffFallbackIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, writer, tool, otherFile string
		linked, fallback              bool
	}{
		{name: "main no edits"},
		{name: "main other agent", writer: "other", tool: "Write"},
		{name: "main unattributed edit", tool: "Edit"},
		{name: "main Bash only", writer: "sub", tool: "Bash"},
		{name: "linked Bash only", writer: "sub", tool: "Bash", linked: true, fallback: true},
		{name: "linked other agent", writer: "other", tool: "Write", linked: true},
		{name: "linked main thread", tool: "Edit", linked: true},
		{name: "linked other scratch write", writer: "other", tool: "Write", linked: true, otherFile: ".tmp/other.go"},
		{name: "linked other local write", writer: "other", tool: "Write", linked: true, otherFile: ".punt-labs/local/other.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			root := store
			path := "<repo>/broken.go"
			if tc.linked {
				root = filepath.Join(store, ".claude", "worktrees", "linked")
				stageGit(t, store, "worktree", "add", "--detach", root)
				t.Setenv("CLAUDE_PROJECT_DIR", root)
			}
			writeFile(t, filepath.Join(root, "broken.go"), "broken Go source\n")
			if tc.otherFile != "" {
				path = "<repo>/" + tc.otherFile
				writeFile(t, filepath.Join(root, tc.otherFile), "package other\n")
			}
			writeFile(t, filepath.Join(store, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, store, "project", "  - name: glob\n    stage: handoff\n    files: ['**/*.go']\n    run: [glob-probe, '{files}']\n  - name: all\n    stage: handoff\n    files: all\n    run: [all-probe]\n")
			log := filepath.Join(t.TempDir(), "log")
			t.Setenv("STAGE_LOG", log)
			postEditTool(t, bin, "glob-probe", "printf 'glob|%s\\n' \"$@\" >> \"$STAGE_LOG\"; exit 1\n")
			postEditTool(t, bin, "all-probe", "printf 'all|%s\\n' \"$PWD\" >> \"$STAGE_LOG\"\n")
			if tc.tool != "" {
				input := map[string]any{"file_path": path}
				if tc.tool == "Bash" {
					input = map[string]any{"command": "printf broken > broken.go"}
				}
				writeRepoTreeSession(t, store, "2026-11-22", "isolation", []auditEntry{{Ts: "2026-11-22T10:00:00Z", Session: "isolation", AgentID: tc.writer, Tool: tc.tool, ToolInput: input, PathFormat: auditPathFormat}})
			}
			ss := session.NewStore(t.TempDir())
			require.NoError(t, ss.Create("isolation", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
			var out, stderr bytes.Buffer
			require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"isolation","agent_id":"sub"}`), ss, &out, &stderr))
			require.Empty(t, stderr.String())
			data, err := os.ReadFile(log)
			require.NoError(t, err)
			want := "all|" + root + "\n"
			if tc.fallback {
				want = "glob|" + filepath.Join(root, "broken.go") + "\n" + want
				require.Contains(t, out.String(), `"decision":"block"`)
			} else {
				require.Empty(t, out.String())
			}
			require.Equal(t, want, string(data))
		})
	}
}

func TestHandoffConcurrentAgents(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: glob\n    stage: handoff\n    files: ['**/*.go']\n    run: [probe, '{files}']\n")
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("STAGE_LOG", log)
	postEditTool(t, bin, "probe", "printf '%s\\n' \"$@\" > \"$STAGE_LOG\"\n")
	var entries []auditEntry
	ss := session.NewStore(t.TempDir())
	require.NoError(t, ss.Create("concurrent", session.Participant{AgentID: "human"}, session.Participant{AgentID: "one"}, "", ""))
	require.NoError(t, ss.Join("concurrent", session.Participant{AgentID: "two"}))
	for i, agent := range []string{"one", "two"} {
		writeFile(t, filepath.Join(root, agent+".go"), "package example\n")
		entries = append(entries, auditEntry{Ts: fmt.Sprintf("2026-11-22T10:00:0%dZ", i), Session: "concurrent", AgentID: agent, Tool: "Edit", ToolInput: map[string]any{"file_path": "<repo>/" + agent + ".go"}, PathFormat: auditPathFormat})
	}
	writeRepoTreeSession(t, root, "2026-11-22", "concurrent", entries)
	for _, agent := range []string{"one", "two"} {
		var out, stderr bytes.Buffer
		payload := fmt.Sprintf(`{"session_id":"concurrent","agent_id":%q}`, agent)
		require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(payload), ss, &out, &stderr))
		require.Empty(t, out.String())
		require.Empty(t, stderr.String())
		data, err := os.ReadFile(log)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(root, agent+".go")+"\n", string(data))
	}
}

func TestHandoffVanishedPaths(t *testing.T) {
	for _, agent := range []string{"sub", "other", ""} {
		for _, parentGone := range []bool{false, true} {
			for _, ownFile := range []bool{false, true} {
				t.Run(fmt.Sprintf("agent=%s/parent-gone=%t/own-file=%t", agent, parentGone, ownFile), func(t *testing.T) {
					root, bin := postEditFixture(t)
					t.Setenv("HOME", t.TempDir())
					writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
					checkPolicy(t, root, "project", "  - name: glob\n    stage: handoff\n    files: ['**/*.go']\n    run: [glob-probe, '{files}']\n  - name: all\n    stage: handoff\n    files: all\n    run: [all-probe]\n")
					log := filepath.Join(t.TempDir(), "log")
					t.Setenv("STAGE_LOG", log)
					postEditTool(t, bin, "glob-probe", "printf 'glob|%s\\n' \"$@\" >> \"$STAGE_LOG\"\n")
					postEditTool(t, bin, "all-probe", "printf 'all\\n' >> \"$STAGE_LOG\"\n")
					removed := filepath.Join(root, "removed", "gone.go")
					writeFile(t, removed, "package gone\n")
					require.NoError(t, os.Remove(removed))
					if parentGone {
						require.NoError(t, os.Remove(filepath.Dir(removed)))
					}
					entries := []auditEntry{{Ts: "2026-11-22T10:00:00Z", Session: "vanished", AgentID: agent, Tool: "Write", ToolInput: map[string]any{"file_path": "<repo>/removed/gone.go"}, PathFormat: auditPathFormat}}
					want := "all\n"
					if ownFile {
						writeFile(t, filepath.Join(root, "own.go"), "package own\n")
						entries = append(entries, auditEntry{Ts: "2026-11-22T10:00:01Z", Session: "vanished", AgentID: "sub", Tool: "Edit", ToolInput: map[string]any{"file_path": "<repo>/own.go"}, PathFormat: auditPathFormat})
						want = "glob|" + filepath.Join(root, "own.go") + "\n" + want
					}
					writeRepoTreeSession(t, root, "2026-11-22", "vanished", entries)
					ss := session.NewStore(t.TempDir())
					require.NoError(t, ss.Create("vanished", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
					var out, stderr bytes.Buffer
					require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"vanished","agent_id":"sub"}`), ss, &out, &stderr))
					require.Empty(t, out.String())
					require.Empty(t, stderr.String())
					data, err := os.ReadFile(log)
					require.NoError(t, err)
					require.Equal(t, want, string(data))
				})
			}
		}
	}
}

func TestHandoffVanishedRootAttribution(t *testing.T) {
	for _, tc := range []struct {
		name, otherPath string
		fromMain, own   bool
		other, glob     bool
	}{
		{name: "own linked root from main", fromMain: true, own: true, glob: true},
		{name: "other vanished parent", other: true},
		{name: "own and other linked roots from main", fromMain: true, own: true, other: true},
		{name: "unresolved symlink", other: true, otherPath: "loop/gone.go"},
		{name: "dangling symlink", other: true, otherPath: "dangling/gone.go"},
		{name: "redacted path", other: true, otherPath: redactedPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			root := filepath.Join(store, ".claude", "worktrees", "linked")
			stageGit(t, store, "worktree", "add", "--detach", root)
			if !tc.fromMain {
				t.Setenv("CLAUDE_PROJECT_DIR", root)
			}
			writeFile(t, filepath.Join(root, "dirty.go"), "package dirty\n")
			require.NoError(t, os.Symlink("loop", filepath.Join(root, "loop")))
			require.NoError(t, os.Symlink("missing", filepath.Join(root, "dangling")))
			writeFile(t, filepath.Join(store, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, store, "project", "  - name: glob\n    stage: handoff\n    files: ['**/*.go']\n    run: [glob-probe, '{files}']\n  - name: all\n    stage: handoff\n    files: all\n    run: [all-probe]\n")
			log := filepath.Join(t.TempDir(), "log")
			t.Setenv("STAGE_LOG", log)
			postEditTool(t, bin, "glob-probe", "printf 'glob|%s\\n' \"$@\" >> \"$STAGE_LOG\"\n")
			postEditTool(t, bin, "all-probe", "printf 'all|%s\\n' \"$PWD\" >> \"$STAGE_LOG\"\n")
			prefix := "<repo>/"
			if tc.fromMain {
				prefix += ".claude/worktrees/linked/"
			}
			var entries []auditEntry
			if tc.own {
				entries = append(entries, auditEntry{Ts: "2026-11-22T10:00:00Z", Session: "roots", AgentID: "sub", Tool: "Write", ToolInput: map[string]any{"file_path": prefix + "removed/deep/own.go"}, PathFormat: auditPathFormat})
			}
			if tc.other {
				path := prefix + "removed/deep/other.go"
				if tc.otherPath == redactedPath {
					path = redactedPath
				} else if tc.otherPath != "" {
					path = prefix + tc.otherPath
				}
				entries = append(entries, auditEntry{Ts: "2026-11-22T10:00:01Z", Session: "roots", AgentID: "other", Tool: "Edit", ToolInput: map[string]any{"file_path": path}, PathFormat: auditPathFormat})
			}
			writeRepoTreeSession(t, store, "2026-11-22", "roots", entries)
			ss := session.NewStore(t.TempDir())
			require.NoError(t, ss.Create("roots", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
			var out, stderr bytes.Buffer
			require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"roots","agent_id":"sub"}`), ss, &out, &stderr))
			require.Empty(t, out.String())
			require.Empty(t, stderr.String())
			data, err := os.ReadFile(log)
			require.NoError(t, err)
			want := "all|" + root + "\n"
			if tc.glob {
				want = "glob|" + filepath.Join(root, "dirty.go") + "\n" + want
			}
			require.Equal(t, want, string(data))
		})
	}
}

func TestHandoffUnreadableAudit(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: all\n    stage: handoff\n    files: all\n    run: [missing]\n")
	path := writeRepoTreeSession(t, root, "2026-11-22", "unreadable", nil)
	require.NoError(t, os.Mkdir(path, 0700))
	ss := session.NewStore(t.TempDir())
	require.NoError(t, ss.Create("unreadable", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
	var out, stderr bytes.Buffer
	require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"unreadable","agent_id":"sub"}`), ss, &out, &stderr))
	require.Contains(t, out.String(), `"decision":"block"`)
	require.Contains(t, out.String(), "handoff files:")
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

func TestCommitPartialStaging(t *testing.T) {
	for _, tc := range []struct {
		name, pattern, index                        string
		stagedBad, diskBad, partial, linked, blocks bool
	}{
		{name: "staged bad disk good", partial: true, stagedBad: true, blocks: true},
		{name: "staged good disk bad", partial: true, diskBad: true, blocks: true},
		{name: "unmatched", pattern: "['**/*.sh']", partial: true},
		{name: "fully staged"},
		{name: "all", pattern: "all", partial: true, blocks: true},
		{name: "linked", partial: true, linked: true, blocks: true},
		{name: "absolute custom index", index: "absolute", partial: true, blocks: true},
		{name: "relative custom index", index: "relative", partial: true, linked: true, blocks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			root := store
			if tc.linked {
				root = filepath.Join(store, ".claude", "worktrees", "linked")
				stageGit(t, store, "worktree", "add", "--detach", root)
			}
			if tc.index != "" {
				index := filepath.Join(root, "custom-index")
				if tc.index == "relative" {
					index = "custom-index"
				}
				t.Setenv("GIT_INDEX_FILE", index)
				stageGit(t, root, "read-tree", "HEAD")
			}
			pattern := tc.pattern
			if pattern == "" {
				pattern = "['**/*.go']"
			}
			writeFile(t, filepath.Join(store, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, store, "project", "  - name: formatting\n    stage: commit\n    files: "+pattern+"\n    on_fail: advisory\n    run: [probe]\n")
			log := filepath.Join(t.TempDir(), "ran")
			t.Setenv("STAGE_LOG", log)
			postEditTool(t, bin, "probe", "printf ran > \"$STAGE_LOG\"\n")
			formatted, bad := "package probe\n\nvar probe = 1\n", "package probe\nvar  probe=1\n"
			for _, name := range []string{"one.go", "two space.go"} {
				staged := formatted
				if tc.stagedBad {
					staged = bad
				}
				writeFile(t, filepath.Join(root, name), staged)
				stageGit(t, root, "add", "--", name)
				if tc.partial {
					disk := formatted
					if tc.diskBad {
						disk = bad
					} else if !tc.stagedBad {
						disk += "// unstaged\n"
					}
					writeFile(t, filepath.Join(root, name), disk)
				}
			}
			if tc.pattern == "['**/*.sh']" {
				writeFile(t, filepath.Join(root, "matched.sh"), "#!/bin/sh\n")
				stageGit(t, root, "add", "matched.sh")
			}
			feedback := RunStageChecks(context.Background(), root, "", "commit")
			require.Empty(t, feedback.Advisory)
			_, ran := os.Stat(log)
			if tc.blocks {
				require.Contains(t, feedback.Blocking, "has unstaged changes")
				require.Contains(t, feedback.Blocking, "Stage or stash")
				require.Contains(t, feedback.Blocking, "git stash --keep-index")
				require.Contains(t, feedback.Blocking, "one.go")
				require.Contains(t, feedback.Blocking, "two space.go")
				require.ErrorIs(t, ran, os.ErrNotExist)
			} else {
				require.Empty(t, feedback.Blocking)
				require.NoError(t, ran)
			}
		})
	}
}

func TestCommitPartialStagingQueryFailure(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: probe\n    stage: commit\n    files: all\n    on_fail: advisory\n    run: [probe]\n")
	log := filepath.Join(t.TempDir(), "ran")
	t.Setenv("STAGE_LOG", log)
	postEditTool(t, bin, "probe", "printf ran > \"$STAGE_LOG\"\n")
	feedback := runStageChecks(context.Background(), root, "", "commit", func() ([]string, error) {
		writeFile(t, filepath.Join(root, ".git", "HEAD"), "invalid\n")
		return []string{"one.go"}, nil
	})
	require.Contains(t, feedback.Blocking, "git diff unstaged files")
	require.Empty(t, feedback.Advisory)
	_, err := os.Stat(log)
	require.ErrorIs(t, err, os.ErrNotExist)
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
			writeFile(t, filepath.Join(linked, ".punt-labs/ethos.yaml"), "checks: ''\n")
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
