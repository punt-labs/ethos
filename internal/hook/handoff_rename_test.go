//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/require"
)

func TestHandoffRenamedFiles(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, kind := range []string{"mv", "git mv", "delete", "lookup failure", "weird names", "split index", "borrowed index", "target owned by other agent", "modified mv", "similar target", "unattributed similar target", "unattributed delete"} {
			t.Run(fmt.Sprintf("linked=%t/%s", linked, kind), func(t *testing.T) {
				store, bin := postEditFixture(t)
				t.Setenv("HOME", t.TempDir())
				old, target := "a.go", "b.go"
				if kind == "weird names" {
					old, target = "-old file.go", "-new\nfile.go"
				}
				body := "package sample\n" + strings.Repeat("// original line\n", 40)
				writeFile(t, filepath.Join(store, old), body)
				stageGit(t, store, "add", "--", old)
				stageGit(t, store, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "tracked source")
				root := store
				if linked {
					root = filepath.Join(store, ".claude/worktrees/linked")
					stageGit(t, store, "worktree", "add", "--detach", root)
					t.Setenv("CLAUDE_PROJECT_DIR", root)
				}
				if kind == "split index" {
					stageGit(t, root, "update-index", "--split-index")
				}
				content := body
				if kind == "modified mv" || strings.Contains(kind, "similar target") {
					content += "// agent edit\n"
				}
				writeFile(t, filepath.Join(root, old), content)
				if kind == "delete" || kind == "unattributed delete" {
					require.NoError(t, os.Remove(filepath.Join(root, old)))
				} else if strings.Contains(kind, "similar target") {
					require.NoError(t, os.Remove(filepath.Join(root, old)))
					writeFile(t, filepath.Join(root, target), body+"// unrelated generated file\n")
				} else if kind == "git mv" {
					stageGit(t, root, "mv", "--", old, target)
				} else {
					require.NoError(t, os.Rename(filepath.Join(root, old), filepath.Join(root, target)))
				}
				writeFile(t, filepath.Join(root, "other.go"), "package unrelated\n")
				writeFile(t, filepath.Join(store, ".punt-labs/ethos.yaml"), "checks: project\n")
				checkPolicy(t, store, "project", "  - name: glob\n    stage: handoff\n    files: ['**/*.go']\n    run: [probe, '{files}']\n  - name: all\n    stage: handoff\n    files: all\n    run: [all-probe]\n")
				log, all := filepath.Join(t.TempDir(), "files"), filepath.Join(t.TempDir(), "all")
				t.Setenv("RENAME_LOG", log)
				t.Setenv("RENAME_ALL", all)
				postEditTool(t, bin, "probe", "printf '%s\\000' \"$@\" >> \"$RENAME_LOG\"\n")
				postEditTool(t, bin, "all-probe", "printf all >> \"$RENAME_ALL\"\n")
				otherPath := "other.go"
				if kind == "target owned by other agent" {
					otherPath = target
				}
				entries := []auditEntry{
					{Ts: "2026-11-22T10:00:00Z", Session: "rename", AgentID: "sub", Tool: "Edit", ToolInput: map[string]any{"file_path": "<repo>/" + old}, PathFormat: auditPathFormat},
				}
				if !strings.HasPrefix(kind, "unattributed ") {
					entries = append(entries, auditEntry{Ts: "2026-11-22T10:00:01Z", Session: "rename", AgentID: "other", Tool: "Write", ToolInput: map[string]any{"file_path": "<repo>/" + otherPath}, PathFormat: auditPathFormat})
				}
				writeRepoTreeSession(t, store, "2026-11-22", "rename", entries)
				data, err := gitenv.Output(root, "git", "rev-parse", "--path-format=absolute", "--git-path", "index")
				require.NoError(t, err)
				indexPath, err := gitenv.IndexPathOutput(data)
				require.NoError(t, err)
				before, err := os.ReadFile(indexPath)
				require.NoError(t, err)
				if kind == "borrowed index" {
					foreign := filepath.Join(t.TempDir(), "index")
					writeFile(t, foreign, "not this checkout's index")
					t.Setenv("GIT_INDEX_FILE", foreign)
					t.Cleanup(func() {
						data, err := os.ReadFile(foreign)
						require.NoError(t, err)
						require.Equal(t, "not this checkout's index", string(data))
					})
				}
				if kind == "lookup failure" {
					realGit, err := exec.LookPath("git")
					require.NoError(t, err)
					realGit, err = filepath.EvalSymlinks(realGit)
					require.NoError(t, err)
					require.NoError(t, os.Remove(filepath.Join(bin, "git")))
					postEditTool(t, bin, "git", "for arg do if [ \"$arg\" = --find-renames ]; then exit 86; fi; done\nexec "+fmt.Sprintf("%q", realGit)+" \"$@\"\n")
				}
				ss := session.NewStore(t.TempDir())
				require.NoError(t, ss.Create("rename", session.Participant{AgentID: "human"}, session.Participant{AgentID: "sub"}, "", ""))
				var out, stderr bytes.Buffer
				require.NoError(t, HandleSubagentStopWithIO(context.Background(), strings.NewReader(`{"session_id":"rename","agent_id":"sub"}`), ss, &out, &stderr))
				require.Empty(t, out.String())
				if kind == "lookup failure" {
					require.Contains(t, stderr.String(), "handoff rename lookup")
				} else if kind == "modified mv" || strings.Contains(kind, "similar target") {
					require.Contains(t, stderr.String(), "non-exact")
				} else {
					require.Empty(t, stderr.String())
				}
				if kind == "delete" || kind == "unattributed delete" || kind == "lookup failure" || kind == "target owned by other agent" || kind == "modified mv" || strings.Contains(kind, "similar target") {
					require.NoFileExists(t, log)
				} else {
					data, err := os.ReadFile(log)
					require.NoError(t, err)
					require.Equal(t, filepath.Join(root, target)+"\x00", string(data))
				}
				require.FileExists(t, all)
				after, err := os.ReadFile(indexPath)
				require.NoError(t, err)
				require.Equal(t, before, after, "real index must remain byte-identical")
			})
		}
	}
}

func TestParseHandoffRenames(t *testing.T) {
	got, err := parseHandoffRenames("M\x00changed.go\x00R100\x00old name.go\x00-new\nname.go\x00C100\x00copy.go\x00copied.go\x00D\x00deleted.go\x00")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"old name.go": "-new\nname.go"}, got)
	got, err = parseHandoffRenames("")
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = parseHandoffRenames("R093\x00deleted.go\x00unrelated.go\x00R100\x00old.go\x00new.go\x00")
	require.ErrorContains(t, err, "non-exact")
	require.Equal(t, map[string]string{"old.go": "new.go"}, got)
	for _, input := range []string{"R100", "R100\x00old\x00", "M\x00", "R100\x00old\x00new", "M\x00\x00"} {
		_, err := parseHandoffRenames(input)
		require.Error(t, err)
	}
}
