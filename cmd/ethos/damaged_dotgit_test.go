//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// damagedShapes build a .git that git does not accept as a repository.
var damagedShapes = map[string]func(dot string) error{
	"HEAD empty":      gitDirWith("HEAD=", "objects/", "refs/"),
	"HEAD garbage":    gitDirWith("HEAD=garbage\n", "objects/", "refs/"),
	"HEAD missing":    gitDirWith("objects/", "refs/"),
	"HEAD a dir":      gitDirWith("HEAD/", "objects/", "refs/"),
	"objects missing": gitDirWith("HEAD=ref: refs/heads/main\n", "refs/"),
	"refs a file":     gitDirWith("HEAD=ref: refs/heads/main\n", "objects/", "refs="),
	"FIFO":            func(dot string) error { return syscall.Mkfifo(dot, 0o644) },
}

func gitDirWith(entries ...string) func(dot string) error {
	return func(dot string) error {
		if err := os.Mkdir(dot, 0o755); err != nil {
			return err
		}
		for _, e := range entries {
			if name, ok := strings.CutSuffix(e, "/"); ok {
				if err := os.Mkdir(filepath.Join(dot, name), 0o755); err != nil {
					return err
				}
				continue
			}
			name, content, _ := strings.Cut(e, "=")
			if err := os.WriteFile(filepath.Join(dot, name), []byte(content), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

// damagedPlace is where the damaged .git sits: in the checkout itself, with
// nothing above it, or inside an outer repository that holds the store.
type damagedPlace struct {
	se  *cliSubprocessEnv
	dir string // the working directory
	dot string // the damaged .git
}

func damagedPlaces(t *testing.T, mk func(dot string) error) map[string]damagedPlace {
	t.Helper()
	alone := setupCLISubprocessEnv(t)
	require.NoError(t, os.RemoveAll(filepath.Join(alone.repo, ".git")))
	dot := filepath.Join(alone.repo, ".git")
	require.NoError(t, mk(dot))
	// The ceiling keeps git from answering for a repository above the
	// temporary directory.
	alone.env = append(alone.env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(alone.repo))

	nested, inner := nestedGitEnv(t, mk)
	return map[string]damagedPlace{
		"alone":  {alone, alone.repo, dot},
		"nested": {nested, inner, filepath.Join(inner, ".git")},
	}
}

func runEthosIn(t *testing.T, p damagedPlace, stdin string, args ...string) (string, int) {
	t.Helper()
	c := exec.Command(ethosBinary, args...)
	c.Dir = p.dir
	c.Env = withForcedNotUnderClaudeCode(p.se.env)
	c.Stdin = strings.NewReader(stdin)
	var b bytes.Buffer
	c.Stdout, c.Stderr = &b, &b
	err := c.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return b.String(), ee.ExitCode()
	}
	require.NoError(t, err)
	return b.String(), 0
}

// A .git that is there and that git does not accept is never read as "not in a
// repository" (the global store) or as the enclosing repository: every store
// command refuses with exit 2 and names the .git.
func TestDamagedDotGitIsRefused(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for name, mk := range damagedShapes {
		t.Run(name, func(t *testing.T) {
			for where, p := range damagedPlaces(t, mk) {
				t.Run(where, func(t *testing.T) {
					global := filepath.Join(p.se.home, ".punt-labs", "ethos", "identities")

					out, code := runEthosIn(t, p, "", "identity", "get", "test-agent")
					assert.Equal(t, 2, code, out)
					assert.NotContains(t, out, "Test Agent")
					assert.Contains(t, out, p.dot)
					assert.Contains(t, out, "git does not accept it as a repository")

					out, code = createIdentityFrom(t, p.se, p.dir)
					assert.Equal(t, 2, code, out)
					assert.Contains(t, out, p.dot)
					assert.NoFileExists(t, filepath.Join(global, "nested.yaml"), "nothing written to the global store")
					assert.NoFileExists(t, filepath.Join(p.se.repo, ".punt-labs", "ethos", "identities", "nested.yaml"))

					out, code = runEthosIn(t, p, "", "doctor")
					assert.Contains(t, out, p.dot)
					assert.NotEqual(t, 0, code, out)
				})
			}
		})
	}
}

// session-start tells the session why there is no repository store, and writes
// nothing into the enclosing repository.
func TestDamagedDotGitSessionStart(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for name, mk := range damagedShapes {
		t.Run(name, func(t *testing.T) {
			for where, p := range damagedPlaces(t, mk) {
				t.Run(where, func(t *testing.T) {
					const sid = "damaged-s1"
					stdout, stderr, _ := runHookIn(t, p.se, p.dir, "session-start",
						`{"session_id":"`+sid+`","hook_event_name":"SessionStart"}`,
						"GIT_CEILING_DIRECTORIES="+filepath.Dir(p.se.repo))
					assert.Contains(t, stdout+stderr, p.dot)
					assert.Empty(t, pathsNaming(t, p.se.repo, sid), "no session in the enclosing repository")
				})
			}
		})
	}
}

// serve names the .git on a read and refuses a write, rather than serving the
// global store or the enclosing repository.
func TestDamagedDotGitServe(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for name, mk := range damagedShapes {
		t.Run(name, func(t *testing.T) {
			for where, p := range damagedPlaces(t, mk) {
				t.Run(where, func(t *testing.T) {
					t.Setenv("HOME", p.se.home)
					t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(p.se.repo))
					t.Chdir(p.dir)
					srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
					buildServeHandler(p.se.home, locateServeStore()).RegisterTools(srv)
					var req mcp.CallToolRequest
					req.Params.Name = "identity"
					req.Params.Arguments = map[string]any{"method": "create"}
					res, err := srv.GetTool("identity").Handler(context.Background(), req)
					require.NoError(t, err)
					require.True(t, res.IsError)
					assert.Contains(t, res.Content[0].(mcp.TextContent).Text, p.dot)
				})
			}
		})
	}
}
