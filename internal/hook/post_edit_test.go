//go:build linux || darwin

package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPostEditGeneratedCommand(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude", "agents", "bwk.md"))
	require.Equal(t, `if [ -f "$HOME/.punt-hooks-kill" ]; then exit 0; fi; f=$(mktemp) || exit 0; c= w=; trap 'if [ -n "$w" ]; then kill "$w" 2>/dev/null; wait "$w" 2>/dev/null; fi; if [ -n "$c" ]; then kill "$c" 2>/dev/null; wait "$c" 2>/dev/null; fi; rm -f "$f"' 0; trap 'exit 0' 1 2 15; exec 3<&0; cat <&3 3<&- >"$f" & c=$!; exec 3<&-; (trap 'kill "$!" 2>/dev/null; wait "$!" 2>/dev/null; exit 0' 1 2 15; sleep 10 & wait "$!"; kill "$c" 2>/dev/null) </dev/null >/dev/null 2>&1 & w=$!; wait "$c" 2>/dev/null; c=; kill "$w" 2>/dev/null; wait "$w" 2>/dev/null; w=; cat "$f" | ethos hook post-edit --agent 'bwk' || true`, command)
	data, err := os.ReadFile(filepath.Join(root, ".claude", "agents", "bwk.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "timeout: 70")
}

func postEditFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	initGitRepo(t, root)
	bin := t.TempDir()
	git, err := exec.LookPath("git")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(git, filepath.Join(bin, "git")))
	for _, name := range []string{"mktemp", "sleep", "rm"} {
		path, err := exec.LookPath(name)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(path, filepath.Join(bin, name)))
	}
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	t.Setenv("PATH", bin)
	return root, bin
}

func postEditRun(t *testing.T, path string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"tool_input": map[string]any{"file_path": path}})
	require.NoError(t, err)
	var out bytes.Buffer
	PostEdit(context.Background(), bytes.NewReader(payload), &out)
	return out.String()
}

func postEditTool(t *testing.T, bin, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o700))
}

func TestPostEditScope(t *testing.T) {
	root, bin := postEditFixture(t)
	foreign := t.TempDir()
	initGitRepo(t, foreign)
	worktree := filepath.Join(t.TempDir(), "linked")
	cmd := exec.Command("git", "worktree", "add", "--detach", worktree)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.NoError(t, os.Symlink(foreign, filepath.Join(root, "escape")))
	marker := filepath.Join(t.TempDir(), "hostile-ran")
	t.Setenv("POST_EDIT_MARKER", marker)
	postEditTool(t, bin, "shellcheck", "printf 'ran' > \"$POST_EDIT_MARKER\"\n")
	for _, tt := range []struct {
		name, path string
		runs       bool
	}{
		{"relative", "file.sh", true},
		{"absolute", filepath.Join(root, "file.sh"), true},
		{"linked worktree", filepath.Join(worktree, "file.sh"), true},
		{"foreign repo", filepath.Join(foreign, "file.sh"), false},
		{"symlink escape", filepath.Join(root, "escape", "file.sh"), false},
		{"scratch", ".tmp/file.sh", false},
		{"local", ".punt-labs/local/file.sh", false},
		{"linked scratch", filepath.Join(worktree, ".tmp/file.sh"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600))
			_ = os.Remove(marker)
			require.Empty(t, postEditRun(t, tt.path))
			_, err := os.Stat(marker)
			if tt.runs {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestPostEditVanishedParentScope(t *testing.T) {
	root, _ := postEditFixture(t)
	linked := filepath.Join(root, ".claude", "worktrees", "linked")
	stageGit(t, root, "worktree", "add", "--detach", linked)
	require.NoError(t, os.Mkdir(filepath.Join(linked, "deep"), 0700))
	require.NoError(t, os.Symlink(filepath.Join(linked, "deep"), filepath.Join(root, "alias")))
	foreign := t.TempDir()
	initGitRepo(t, foreign)
	require.NoError(t, os.Mkdir(filepath.Join(foreign, "deep"), 0700))
	require.NoError(t, os.Symlink(filepath.Join(foreign, "deep"), filepath.Join(root, "escape")))
	for _, tc := range []struct {
		path, wantRoot, wantFile string
	}{
		{linked + "/removed/deep/file.go", linked, linked + "/removed/deep/file.go"},
		{root + "/alias/../removed/deep/file.go", linked, linked + "/removed/deep/file.go"},
		{root + "/escape/../removed/deep/file.go", "", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			gotRoot, gotFile, err := postEditScope(root, tc.path)
			require.NoError(t, err)
			require.Equal(t, tc.wantRoot, gotRoot)
			require.Equal(t, tc.wantFile, gotFile)
		})
	}
}

func TestPostEditNestedWorktreeRoot(t *testing.T) {
	root, bin := postEditFixture(t)
	worktree := filepath.Join(root, ".claude", "worktrees", "nested")
	cmd := exec.Command("git", "worktree", "add", "--detach", worktree)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	writeFile(t, filepath.Join(worktree, "go.mod"), "module example.com/nested\n")
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/session\n")
	writeFile(t, filepath.Join(worktree, "file.go"), "package nested\n")
	log := filepath.Join(root, "vet-cwd")
	t.Setenv("POST_EDIT_LOG", log)
	postEditTool(t, bin, "go", "printf '%s\\n' \"$PWD\" > \"$POST_EDIT_LOG\"\n")
	require.Empty(t, postEditRun(t, filepath.Join(worktree, "file.go")))
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, worktree+"\n", string(data))
	for _, excluded := range []string{".tmp/file.go", ".punt-labs/local/file.go"} {
		writeFile(t, filepath.Join(worktree, excluded), "package scratch\n")
		require.NoError(t, os.Remove(log))
		require.Empty(t, postEditRun(t, filepath.Join(worktree, excluded)))
		_, err := os.Stat(log)
		require.ErrorIs(t, err, os.ErrNotExist)
		writeFile(t, log, "")
	}
}

func TestPostEditSelectionAndLiteralArguments(t *testing.T) {
	for _, tc := range []struct{ name, marker, file, tools, want string }{
		{"Go", "go.mod", "sub/file.go", "gofmt,go", "gofmt|-l|{file}\ngo|vet|./sub\n"},
		{"root Go", "go.mod", "-$(x) file.go", "gofmt,go", "gofmt|-l|{file}\ngo|vet|./\n"},
		{"Python", "pyproject.toml", "file.py", "ruff,uv", "ruff|check|{file}\n"},
		{"uv fallback", "pyproject.toml", "file.py", "uv", "uv|run|ruff|check|{file}\n"},
		{"shell", "", "-$(x) file.sh", "shellcheck", "shellcheck|{file}\n"},
		{"no marker", "", "file.go", "go,gofmt", ""},
		{"setup not selected", "setup.py", "file.py", "ruff", ""},
		{"missing tools", "go.mod", "file.go", "", ""},
		{"unmatched", "go.mod", "file.yaml", "go,gofmt", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := postEditFixture(t)
			if tc.marker != "" {
				writeFile(t, filepath.Join(root, tc.marker), "")
			}
			file := filepath.Join(root, tc.file)
			writeFile(t, file, "")
			log := filepath.Join(root, "calls")
			t.Setenv("POST_EDIT_LOG", log)
			t.Setenv("GIT_TRACE_TEST", "must-be-stripped")
			for _, tool := range strings.Split(tc.tools, ",") {
				if tool == "" {
					continue
				}
				postEditTool(t, bin, tool, "[ -z \"$GIT_TRACE_TEST$GIT_DIR$GIT_INDEX_FILE\" ] || exit 9\nline='"+tool+"'\nfor arg do line=\"$line|$arg\"; done\nprintf '%s\\n' \"$line\" >> \"$POST_EDIT_LOG\"\n")
			}
			require.Empty(t, postEditRun(t, tc.file))
			data, err := os.ReadFile(log)
			if tc.want == "" {
				require.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			require.NoError(t, err)
			// Go checks run concurrently: compare the lines without ordering.
			require.ElementsMatch(t, strings.Split(strings.TrimSpace(strings.ReplaceAll(tc.want, "{file}", file)), "\n"), strings.Split(strings.TrimSpace(string(data)), "\n"))
		})
	}
}

func TestPostEditAdvisory(t *testing.T) {
	root, bin := postEditFixture(t)
	writeFile(t, filepath.Join(root, "go.mod"), "")
	writeFile(t, filepath.Join(root, "file.go"), "")
	postEditTool(t, bin, "gofmt", "printf 'unformatted\\n'\n")
	postEditTool(t, bin, "go", "i=1; while [ $i -le 45 ]; do printf 'LINE_%s\\n' \"$i\"; i=$((i+1)); done; echo 'api_key=topsecretvalue'; exit 1\n")
	out := postEditRun(t, "file.go")
	var response struct {
		HookSpecificOutput struct{ HookEventName, AdditionalContext string }
	}
	require.NoError(t, json.Unmarshal([]byte(out), &response))
	require.Equal(t, "PostToolUse", response.HookSpecificOutput.HookEventName)
	context := response.HookSpecificOutput.AdditionalContext
	require.Contains(t, context, "gofmt")
	require.Contains(t, context, "go vet ./")
	require.Contains(t, context, "unformatted")
	require.Contains(t, context, "LINE_45")
	require.NotContains(t, context, "LINE_1\n")
	require.NotContains(t, context, "topsecretvalue")
}

func TestPostEditFailOpen(t *testing.T) {
	_, _ = postEditFixture(t)
	for _, payload := range []string{"{bad", "{}", `{"tool_input":{"file_path":42}}`} {
		var out bytes.Buffer
		PostEdit(context.Background(), strings.NewReader(payload), &out)
		require.Contains(t, out.String(), "additionalContext")
	}
	require.NoError(t, os.Remove(filepath.Join(os.Getenv("PATH"), "git")))
	postEditTool(t, os.Getenv("PATH"), "git", "echo git-broke >&2; exit 1\n")
	require.Contains(t, postEditRun(t, "file.sh"), "additionalContext")
}

func TestPostEditTimeout(t *testing.T) {
	root, bin := postEditFixture(t)
	writeFile(t, filepath.Join(root, "file.sh"), "")
	sleep, err := exec.LookPath("/bin/sleep")
	require.NoError(t, err)
	postEditTool(t, bin, "shellcheck", "exec "+sleep+" 30\n")
	start := time.Now()
	out := postEditRun(t, "file.sh")
	require.Contains(t, out, "timed out")
	require.Less(t, time.Since(start), 15*time.Second)
}

func TestPostEditKillSwitch(t *testing.T) {
	root, bin := postEditFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".punt-hooks-kill"), nil, 0o600))
	marker := filepath.Join(t.TempDir(), "ran")
	t.Setenv("POST_EDIT_MARKER", marker)
	postEditTool(t, bin, "shellcheck", "printf 'ran' > \"$POST_EDIT_MARKER\"\nexit 1\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.sh"), []byte("#!/bin/sh\n"), 0o600))
	require.Empty(t, postEditRun(t, "file.sh"))
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
}
