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

	t.Run("failure surfaces on stderr and exits 2", func(t *testing.T) {
		const marker = "STATICCHECK_FAILURE_XYZZY"
		binDir := stubMake(t, "echo "+marker+"\nexit 1\n")

		code, stdout, stderr := runHook(t, command, binDir, stdin)

		assert.Equal(t, 2, code, "blocking exit code must be 2")
		assert.Contains(t, stderr, marker, "failing tool output must reach stderr")
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
