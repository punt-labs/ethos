//go:build linux || darwin

package hook

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestPostToolUseHook_Execution runs the emitted PostToolUse hook
// command under /bin/sh with a stub make on PATH. Pinning the command
// text (TestGenerateAgentFiles) proves the string is stable but not
// that it behaves: a logic inversion (-eq vs -ne), broken if/fi
// nesting, or a dash incompatibility would still pass a text match.
// This test executes the command and checks the exit code and streams.
//
// The stub make lets the test control make check's outcome without the
// repo's real Makefile, so it is hermetic: temp project dir, temp PATH,
// no network.
func TestPostToolUseHook_Execution(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))

	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude", "agents", "bwk.md"))

	// A single-branch revert to the masking form (`exit $_rc`) would
	// swallow the failure detail — the exact bug ethos-bo84 fixes.
	assert.NotContains(t, command, "exit $_rc",
		"command must not pipe make check straight to exit (masks failure)")

	// The hook feeds on the Write/Edit tool payload via stdin. A Go
	// source path routes to the make-check branch whether or not jq is
	// installed: with jq the *.go case matches; without jq the no-jq
	// branch runs make check unconditionally.
	stdin := `{"tool_input":{"file_path":"internal/foo.go"}}`

	t.Run("failure surfaces the tail on stderr and exits 2", func(t *testing.T) {
		// A real make check failure buries its diagnostic at the END of
		// output, after >60 lines of passing-stage noise. The stub emits
		// 70 numbered lines then exits 1: with `tail -n 60` stderr holds
		// LINE_011..LINE_070, so the last line survives and the first is
		// truncated. A revert to `head -n 60` would invert this — keeping
		// LINE_001 and dropping LINE_070 — and fail these assertions.
		binDir := stubMake(t, "i=1\nwhile [ \"$i\" -le 70 ]; do\n  printf 'LINE_%03d\\n' \"$i\"\n  i=$((i + 1))\ndone\nexit 1\n")

		code, stdout, stderr := runHook(t, command, binDir, stdin)

		assert.Equal(t, 2, code, "blocking exit code must be 2")
		assert.Contains(t, stderr, "LINE_070", "tail must keep the last line (the real diagnostic)")
		assert.NotContains(t, stderr, "LINE_001", "tail must truncate the leading passing-stage noise")
		assert.Empty(t, stdout, "nothing must go to stdout")
	})

	t.Run("success is silent and exits 0", func(t *testing.T) {
		binDir := stubMake(t, "exit 0\n")

		code, stdout, stderr := runHook(t, command, binDir, stdin)

		assert.Equal(t, 0, code, "success exit code must be 0")
		assert.Empty(t, stdout, "success must be silent on stdout")
		assert.Empty(t, stderr, "success must be silent on stderr")
	})
}

// extractPostToolUseCommand parses the agent file's YAML frontmatter
// and returns the PostToolUse hook command with YAML escaping undone.
func extractPostToolUseCommand(t *testing.T, agentPath string) string {
	t.Helper()

	data, err := os.ReadFile(agentPath)
	require.NoError(t, err)

	// Frontmatter is the block between the first two "---" lines.
	content := string(data)
	require.True(t, strings.HasPrefix(content, "---\n"), "agent file must open with frontmatter")
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	require.GreaterOrEqual(t, end, 0, "frontmatter must close with ---")
	frontmatter := rest[:end]

	var fm struct {
		Hooks struct {
			PostToolUse []struct {
				Hooks []struct {
					Command string `yaml:"command"`
				} `yaml:"hooks"`
			} `yaml:"PostToolUse"`
		} `yaml:"hooks"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(frontmatter), &fm))
	require.Len(t, fm.Hooks.PostToolUse, 1)
	require.Len(t, fm.Hooks.PostToolUse[0].Hooks, 1)

	cmd := fm.Hooks.PostToolUse[0].Hooks[0].Command
	require.NotEmpty(t, cmd)
	return cmd
}

// stubMake writes an executable `make` script into a fresh temp dir and
// returns the dir, for prepending to PATH. body is the shell after the
// shebang; the stub ignores its arguments.
func stubMake(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "make")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	return dir
}

// runHook executes command under /bin/sh with binDir prepended to PATH
// and stdin fed to the process, returning the exit code and streams.
func runHook(t *testing.T, command, binDir, stdin string) (code int, stdout, stderr string) {
	t.Helper()

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CLAUDE_PROJECT_DIR="+t.TempDir(),
	)
	cmd.Stdin = strings.NewReader(stdin)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, "hook must exit cleanly, not fail to start")
		code = exitErr.ExitCode()
	}
	return code, outBuf.String(), errBuf.String()
}

// TestPostToolUseHook_WorktreeResolution proves ethos-n4tk behaviorally:
// when a sub-agent edits a file inside a linked git worktree, the hook
// runs make check in THAT worktree, not in $CLAUDE_PROJECT_DIR (the main
// checkout). A logic error in the git-resolution branch — wrong dirname,
// a fallback that fires when it should not, or a cd to the wrong root —
// would still pass the command-text pin in TestGenerateAgentFiles but
// fail here.
//
// The proof is a sentinel Makefile in each tree. Each check target echoes
// a tree-unique word and exits 1, so the failing branch surfaces the word
// on stderr and the assertion reads which tree's make check actually ran.
// $CLAUDE_PROJECT_DIR points at the main tree in both cases, so a hook
// that ignored the edited path would always print MAIN_TREE.
func TestPostToolUseHook_WorktreeResolution(t *testing.T) {
	requireTool(t, "git")
	requireTool(t, "make")
	// The matched-path and empty-path branches are only reached when jq
	// is present; without it the hook takes the no-jq branch and runs
	// make check in $CLAUDE_PROJECT_DIR unconditionally, so there is
	// nothing to prove.
	requireTool(t, "jq")

	root, ids, teams, roles := setupTestRepo(t)
	require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))
	command := extractPostToolUseCommand(t, filepath.Join(root, ".claude", "agents", "bwk.md"))

	// Build the git trees under a base OFF the repo's .tmp directory.
	// TMPDIR points at .tmp here, so a t.TempDir() edited-file path would
	// contain a "/.tmp/" segment and hit the hook's scratch-path bypass
	// (*/.tmp/*) — a test artifact, since real worktrees never live under
	// .tmp. A home-anchored base avoids both bypass patterns.
	base := mkBaseDir(t)
	main := filepath.Join(base, "main")
	require.NoError(t, os.MkdirAll(main, 0o755))
	initGitRepo(t, main) // commits a README
	writeFile(t, filepath.Join(main, "go.mod"), "module example.com/main\n")
	// Sentinel Makefile per tree: the check target echoes a tree-unique
	// word and exits 1, so the failing branch names the tree that ran.
	// The Makefile need not be committed — make reads the working file.
	writeFile(t, filepath.Join(main, "Makefile"), "check:\n\t@echo MAIN_TREE; exit 1\n")

	// Linked worktree with its own sentinel Makefile. --detach avoids the
	// branch-name derivation git otherwise does from the path basename.
	wt := filepath.Join(base, "wt")
	gitCmd(t, main, "worktree", "add", "--detach", wt)
	writeFile(t, filepath.Join(wt, "Makefile"), "check:\n\t@echo WORKTREE_TREE; exit 1\n")

	tests := []struct {
		name      string
		editPath  string // absolute path of the edited file; empty => omit
		wantTree  string // sentinel that must appear on stderr
		otherTree string // sentinel that must NOT appear
	}{
		{
			name:      "edit inside worktree runs make check in the worktree",
			editPath:  filepath.Join(wt, "internal", "svc.go"),
			wantTree:  "WORKTREE_TREE",
			otherTree: "MAIN_TREE",
		},
		{
			name:      "no file path falls back to CLAUDE_PROJECT_DIR",
			editPath:  "",
			wantTree:  "MAIN_TREE",
			otherTree: "WORKTREE_TREE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdin string
			if tt.editPath != "" {
				// The file's directory must exist so git -C can resolve.
				writeFile(t, tt.editPath, "package internal\n")
				stdin = `{"tool_input":{"file_path":"` + tt.editPath + `"}}`
			} else {
				stdin = `{"tool_input":{}}`
			}

			code, stdout, stderr := runHookInProject(t, command, stdin, main)

			assert.Equal(t, 2, code, "make check failure must block with exit 2")
			assert.Contains(t, stderr, tt.wantTree,
				"make check must run in the %s tree", tt.wantTree)
			assert.NotContains(t, stderr, tt.otherTree,
				"make check must not run in the %s tree", tt.otherTree)
			assert.Empty(t, stdout, "nothing must go to stdout")
		})
	}
}

// runHookInProject executes command under /bin/sh with CLAUDE_PROJECT_DIR
// set to projectDir and stdin fed to the process. Unlike runHook it does
// not stub make on PATH — the worktree test uses real make against the
// sentinel Makefiles it writes into each tree.
func runHookInProject(t *testing.T, command, stdin, projectDir string) (code int, stdout, stderr string) {
	t.Helper()

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "CLAUDE_PROJECT_DIR="+projectDir)
	cmd.Stdin = strings.NewReader(stdin)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, "hook must exit cleanly, not fail to start")
		code = exitErr.ExitCode()
	}
	return code, outBuf.String(), errBuf.String()
}

// gitCmd runs git in dir under the same isolated environment initGitRepo
// uses (HOME pinned to dir, system/global config ignored, fixed identity),
// failing the test on any git error.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(dir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}

// requireTool skips the test when name is not on PATH, so the behavioral
// test is a no-op on a host that lacks the runtime tooling the hook needs.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not found on PATH; skipping", name)
	}
}

// mkBaseDir returns a temp directory guaranteed to be free of a ".tmp" or
// ".punt-labs/ethos" path segment, so edited-file paths built under it do
// not trip the hook's scratch-path bypass. t.TempDir() cannot be used
// because TMPDIR is the repo's .tmp directory. The directory is anchored
// in the user's home and removed at test end.
func mkBaseDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	base, err := os.MkdirTemp(home, ".n4tk-worktree-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	return base
}
