//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

const linkedLegacyIgnore = ".punt-labs/ethos/**/*.local.yaml\n"

// linkedIgnoreEnv starts with neither root protected by the local-zone rule.
// The tracked legacy protection must survive maintenance in both worktrees.
func linkedIgnoreEnv(t *testing.T) (*cliSubprocessEnv, string) {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "the real CLI must build; these regressions must not skip")
	se := freshCLIEnv(t, "ignore-human")
	store := realPath(t, se.repo)
	writeRepoFile(t, store, ".gitignore", linkedLegacyIgnore)
	runGit(t, store, "add", ".gitignore")
	runGit(t, store, "commit", "-q", "-m", "legacy protection only")
	checkout := filepath.Join(realPath(t, t.TempDir()), "linked")
	runGit(t, store, "worktree", "add", "-q", "-b", "linked-ignore", checkout)
	se.repo = checkout
	writeRepoFile(t, se.home, ".punt-labs/ethos/identities/ignore-agent.yaml",
		"name: Ignore Agent\nhandle: ignore-agent\nkind: agent\n")
	for _, root := range []string{store, checkout} {
		covered, err := enable.LocalIgnored(root)
		require.NoError(t, err)
		require.False(t, covered, "fixture must start uncovered at %s", root)
	}
	return se, store
}

func runLinkedIgnoreCLI(t *testing.T, se *cliSubprocessEnv, operation string) (string, int) {
	t.Helper()
	args := []string{"vendor", "ignore-agent", "--apply"}
	if operation != "vendor" {
		answers := writeAnswers(t, t.TempDir(), "name: Ignore Human\nhandle: ignore-human\nemail: ignore@example.test\n")
		args = []string{"setup", "--file", answers}
		if operation == "solo" {
			args = append(args, "--solo")
		}
	}
	_, stderr, code := runCLI(t, se, args...)
	return stderr, code
}

// assertLinkedLocalProtection uses a real overlay write at S followed by ordinary
// git add -A at each root. Neither the overlay nor trace/legacy sentinels may land
// in either index. Public records must still stage normally, without force-add.
func assertLinkedLocalProtection(t *testing.T, se *cliSubprocessEnv, store string) {
	t.Helper()
	const private = "synthetic-private-ignore-regression"
	writeRepoFile(t, store, ".punt-labs/ethos/identities/repo-private.yaml",
		"name: Repo Private\nhandle: repo-private\nkind: agent\n")
	// Layered mode intentionally writes extensions globally. Select repo-only
	// for this independent overlay write so it exercises the repository owner.
	writeRepoFile(t, store, ".punt-labs/ethos.yaml", "resolution: repo-only\n")
	_, stderr, code := runCLI(t, se, "ext", "set", "repo-private", "quarry", "api_token", private, "--local")
	require.Equal(t, 0, code, stderr)
	overlay := ".punt-labs/local/ethos/identities/repo-private.ext/quarry.local.yaml"
	assert.FileExists(t, filepath.Join(store, filepath.FromSlash(overlay)))
	assert.NoFileExists(t, filepath.Join(se.repo, filepath.FromSlash(overlay)), "S is the sole overlay owner")
	for _, root := range []string{store, se.repo} {
		covered, err := enable.LocalIgnored(root)
		require.NoError(t, err)
		assert.True(t, covered, "effective local-zone protection at %s", root)
		body := readGitignore(t, filepath.Join(root, ".gitignore"))
		assert.True(t, strings.HasPrefix(body, linkedLegacyIgnore), "legacy protections at %s", root)
		assert.Equal(t, 1, strings.Count(body, enable.LocalIgnoreRule), "one local rule at %s", root)
		writeRepoFile(t, root, ".punt-labs/local/ethos/trace/missions/m-ignore/prompt.md", private)
		writeRepoFile(t, root, ".punt-labs/ethos/identities/legacy.ext/quarry.local.yaml", private)
		const public = ".punt-labs/ethos/adrs/public.yaml"
		writeRepoFile(t, root, public, "title: public record\n")
		staged := gitAddAll(t, root)
		assert.Contains(t, staged, public, "ordinary add still stages records at %s", root)
		for _, path := range staged {
			assert.NotContains(t, path, ".punt-labs/local/", "private local path staged at %s", root)
			assert.NotContains(t, path, ".local.yaml", "legacy overlay staged at %s", root)
		}
		diff, err := testhelpers.GitCommand("-C", root, "diff", "--cached").Output()
		require.NoError(t, err)
		assert.NotContains(t, string(diff), private, "private bytes staged at %s", root)
		assert.NoFileExists(t, filepath.Join(root, ".punt-labs", "ethos", "enabled"))
	}
	for _, name := range []string{"pre-commit", "commit-msg"} {
		assert.NoFileExists(t, filepath.Join(store, ".git", "hooks", name), "ignore maintenance must not install hooks")
	}
}

func TestCLI_LinkedLocalIgnore(t *testing.T) {
	for _, operation := range []string{"setup", "solo", "vendor"} {
		t.Run(operation, func(t *testing.T) {
			se, store := linkedIgnoreEnv(t)
			if operation != "vendor" {
				_, stderr, code := runCLI(t, se, "seed")
				require.Equal(t, 0, code, stderr)
			}
			for range 2 { // Repeated setup/apply must not duplicate either rule.
				stderr, code := runLinkedIgnoreCLI(t, se, operation)
				require.Equal(t, 0, code, stderr)
			}
			assertLinkedLocalProtection(t, se, store)
		})
	}
}

func TestCLI_LinkedLocalIgnoreFailureAndRetry(t *testing.T) {
	for _, operation := range []string{"setup", "vendor"} {
		for _, host := range []string{"store", "checkout"} {
			t.Run(operation+"/"+host, func(t *testing.T) {
				se, store := linkedIgnoreEnv(t)
				if operation == "setup" {
					_, stderr, code := runCLI(t, se, "seed")
					require.Equal(t, 0, code, stderr)
				}
				root := store
				if host == "checkout" {
					root = se.repo
				}
				path := filepath.Join(root, ".gitignore")
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700)) // Read failure independent of uid.
				stderr, code := runLinkedIgnoreCLI(t, se, operation)
				require.NotZero(t, code, "an uncovered root must not be reported as success")
				assert.Contains(t, stderr, root)
				assert.Contains(t, stderr, ".gitignore")
				require.NoError(t, os.Remove(path))
				writeRepoFile(t, root, ".gitignore", linkedLegacyIgnore)
				stderr, code = runLinkedIgnoreCLI(t, se, operation)
				require.Equal(t, 0, code, stderr)
				assertLinkedLocalProtection(t, se, store)
			})
		}
	}
}

const linkedNestedNegation = "# operator-owned rules\n!local/\nother-user-pattern\n"
const linkedNestedRepair = "# operator-owned rules\nother-user-pattern\n"

// The operator, not setup/vendor, must repair a nested override. Failed retries
// must preserve that intent and must not keep appending ineffective root rules.
func assertLinkedNestedNegation(t *testing.T, root string) string {
	t.Helper()
	body := readGitignore(t, filepath.Join(root, ".gitignore"))
	assert.True(t, strings.HasPrefix(body, linkedLegacyIgnore))
	assert.Equal(t, 1, strings.Count(body, enable.LocalIgnoreRule))
	assert.Equal(t, linkedNestedNegation, readGitignore(t, filepath.Join(root, ".punt-labs", ".gitignore")))
	covered, err := enable.LocalIgnored(root)
	require.NoError(t, err)
	assert.False(t, covered, "nested negation must still defeat the root rule")
	return body
}

func TestCLI_LinkedLocalIgnoreNestedNegation(t *testing.T) {
	for _, operation := range []string{"setup", "solo", "vendor"} {
		for _, host := range []string{"store", "checkout"} {
			t.Run(operation+"/"+host, func(t *testing.T) {
				se, store := linkedIgnoreEnv(t)
				if operation != "vendor" {
					_, stderr, code := runCLI(t, se, "seed")
					require.Equal(t, 0, code, stderr)
				}
				root := store
				if host == "checkout" {
					root = se.repo
				}
				writeRepoFile(t, root, ".punt-labs/.gitignore", linkedNestedNegation)
				var failedBody string
				for attempt := range 2 {
					stderr, code := runLinkedIgnoreCLI(t, se, operation)
					require.NotZero(t, code, "a defeated local-zone rule must not be reported as success")
					assert.Contains(t, stderr, root)
					assert.Contains(t, stderr, enable.LocalIgnoreRule)
					assert.Contains(t, stderr, "still stageable")
					body := assertLinkedNestedNegation(t, root)
					if attempt == 0 {
						failedBody = body
					} else {
						assert.Equal(t, failedBody, body, "failed retry must not rewrite user rules")
					}
				}
				writeRepoFile(t, root, ".punt-labs/.gitignore", linkedNestedRepair)
				stderr, code := runLinkedIgnoreCLI(t, se, operation)
				require.Equal(t, 0, code, stderr)
				assert.Equal(t, linkedNestedRepair, readGitignore(t, filepath.Join(root, ".punt-labs", ".gitignore")))
				assertLinkedLocalProtection(t, se, store)
			})
		}
	}
}

func TestMCPVendor_LinkedLocalIgnoreNestedNegation(t *testing.T) {
	for _, host := range []string{"store", "checkout"} {
		t.Run(host, func(t *testing.T) {
			se, store := linkedIgnoreEnv(t)
			t.Setenv("HOME", se.home)
			t.Setenv("ETHOS_REPO_ROOT", "")
			t.Chdir(se.repo)
			previous := cmdRoots.Load()
			t.Cleanup(func() { cmdRoots.Store(previous) })
			srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
			buildServeHandler(se.home, locateServeStore()).RegisterTools(srv)
			root := store
			if host == "checkout" {
				root = se.repo
			}
			writeRepoFile(t, root, ".punt-labs/.gitignore", linkedNestedNegation)
			tool := srv.GetTool("vendor")
			require.NotNil(t, tool)
			var req mcplib.CallToolRequest
			req.Params.Name = "vendor"
			req.Params.Arguments = map[string]any{"handles": []any{"ignore-agent"}, "apply": true}
			var failedBody string
			for attempt := range 2 {
				result, err := tool.Handler(context.Background(), req)
				require.NoError(t, err)
				require.True(t, result.IsError, "%+v", result)
				require.NotEmpty(t, result.Content)
				text, ok := result.Content[0].(mcplib.TextContent)
				require.True(t, ok)
				assert.Contains(t, text.Text, root)
				assert.Contains(t, text.Text, enable.LocalIgnoreRule)
				assert.Contains(t, text.Text, "still stageable")
				body := assertLinkedNestedNegation(t, root)
				if attempt == 0 {
					failedBody = body
				} else {
					assert.Equal(t, failedBody, body)
				}
			}
			writeRepoFile(t, root, ".punt-labs/.gitignore", linkedNestedRepair)
			result, err := tool.Handler(context.Background(), req)
			require.NoError(t, err)
			require.False(t, result.IsError, "%+v", result)
			assert.Equal(t, linkedNestedRepair, readGitignore(t, filepath.Join(root, ".punt-labs", ".gitignore")))
			assertLinkedLocalProtection(t, se, store)
		})
	}
}

func TestEnsureLocalExtIgnoredDefeatedCanonicalRule(t *testing.T) {
	for _, tc := range []struct{ name, ending string }{
		{"LF", "\n"},
		{"CRLF", "\r\n"},
		{"no-final-newline", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := enableGitRepo(t)
			body := linkedLegacyIgnore + enable.LocalIgnoreRule + tc.ending
			writeRepoFile(t, root, ".gitignore", body)
			writeRepoFile(t, root, ".punt-labs/.gitignore", linkedNestedNegation)
			for range 2 {
				added, err := ensureLocalExtIgnored(root)
				require.Error(t, err)
				assert.False(t, added)
				assert.Contains(t, err.Error(), root)
				assert.Contains(t, err.Error(), enable.LocalIgnoreRule)
				assert.Equal(t, body, assertLinkedNestedNegation(t, root))
			}
		})
	}
}

func TestMCPVendor_LinkedLocalIgnoreCapturedRoots(t *testing.T) {
	for _, obstruction := range []string{"none", "store", "checkout"} {
		t.Run(obstruction, func(t *testing.T) {
			se, store := linkedIgnoreEnv(t)
			t.Setenv("HOME", se.home)
			t.Setenv("ETHOS_REPO_ROOT", "")
			t.Chdir(se.repo)
			previous := cmdRoots.Load()
			t.Cleanup(func() { cmdRoots.Store(previous) })
			srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
			buildServeHandler(se.home, locateServeStore()).RegisterTools(srv)
			captured := currentRoots().Located
			require.Equal(t, store, captured.Store)
			require.Equal(t, se.repo, captured.Checkout)

			// A second request installs different roots. The first handler must
			// not protect that unrelated repository or read replacement config.
			other := realPath(t, t.TempDir())
			runGit(t, other, "init", "-q")
			cmdRoots.Store(&repoRoots{Located: resolve.Located{Store: other, Checkout: other}, Global: true})
			writeRepoFile(t, store, ".punt-labs/ethos.yaml", "record: [malformed\n")
			var blocked string
			if obstruction != "none" {
				root := store
				if obstruction == "checkout" {
					root = se.repo
				}
				blocked = filepath.Join(root, ".gitignore")
				require.NoError(t, os.Remove(blocked))
				require.NoError(t, os.Mkdir(blocked, 0o700))
			}
			tool := srv.GetTool("vendor")
			require.NotNil(t, tool)
			var req mcplib.CallToolRequest
			req.Params.Name = "vendor"
			req.Params.Arguments = map[string]any{"handles": []any{"ignore-agent"}, "apply": true}
			result, err := tool.Handler(context.Background(), req)
			require.NoError(t, err)
			if blocked != "" {
				require.True(t, result.IsError, "%+v", result)
				require.NotEmpty(t, result.Content)
				text, ok := result.Content[0].(mcplib.TextContent)
				require.True(t, ok)
				assert.Contains(t, text.Text, blocked)
				require.NoError(t, os.Remove(blocked))
				writeRepoFile(t, filepath.Dir(blocked), ".gitignore", linkedLegacyIgnore)
				result, err = tool.Handler(context.Background(), req)
				require.NoError(t, err)
			}
			require.False(t, result.IsError, "%+v", result)
			assert.FileExists(t, filepath.Join(store, ".punt-labs", "ethos", "identities", "ignore-agent.yaml"))
			assert.NoFileExists(t, filepath.Join(other, ".gitignore"), "later request's roots must remain untouched")
			// Restore config only for the subsequent independent CLI overlay write.
			require.NoError(t, os.Remove(filepath.Join(store, ".punt-labs", "ethos.yaml")))
			assertLinkedLocalProtection(t, se, store)
		})
	}
}

func TestLocalZonesIgnoreResolvedFailures(t *testing.T) {
	failure := errors.New("captured location failure")
	for _, part := range []string{"store", "checkout", "ethos", "config"} {
		t.Run(part, func(t *testing.T) {
			root := enableGitRepo(t)
			loc := resolve.Located{Store: root, Checkout: root}
			switch part {
			case "store":
				loc.StoreErr = failure
			case "checkout":
				loc.CheckoutErr = failure
			case "ethos":
				loc.EthosErr = failure
			case "config":
				loc.ConfigErr = failure
			}
			added, err := ensureLocalZonesIgnored(loc)
			require.ErrorIs(t, err, failure)
			assert.False(t, added)
			assert.NoFileExists(t, filepath.Join(root, ".gitignore"))
		})
	}
}

func TestLocalZonesIgnoreEqualRoots(t *testing.T) {
	root := enableGitRepo(t)
	loc := resolve.Located{Store: root, Checkout: root}
	added, err := ensureLocalZonesIgnored(loc)
	require.NoError(t, err)
	assert.True(t, added)
	before := readGitignore(t, filepath.Join(root, ".gitignore"))
	assert.Equal(t, 1, strings.Count(before, enable.LocalIgnoreRule))
	added, err = ensureLocalZonesIgnored(loc)
	require.NoError(t, err)
	assert.False(t, added)
	assert.Equal(t, before, readGitignore(t, filepath.Join(root, ".gitignore")))
}
