//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/agentmark"
	"github.com/punt-labs/ethos/v4/plugin/hooks"
)

// execHookSessionStart runs the built `ethos hook session-start` in se's repo.
func execHookSessionStart(t *testing.T, se *cliSubprocessEnv) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(ethosBinary, "hook", "session-start")
	cmd.Dir = se.repo
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	cmd.Stdin = strings.NewReader(`{"session_id":"refresh-cli-session"}`)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	require.NoError(t, cmd.Run(), "stdout: %s\nstderr: %s", out.String(), errOut.String())
	return out.String(), errOut.String()
}

func TestCLI_SessionStartLeavesTrackedAgentsUnchanged(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	_, stderr, code := runCLI(t, se, "enable", "--scope", "project")
	require.Zero(t, code, stderr)
	stale := filepath.Join(se.repo, ".claude", "agents", "retired.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o755))
	require.NoError(t, os.WriteFile(stale, []byte(agentmark.Add("---\nname: retired\n---\n")), 0o644))
	store := filepath.Join(se.repo, ".punt-labs", "ethos")
	writeRefreshTeam(t, se.repo, store)
	stub := filepath.Join(store, "agents", "legacy-only.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(stub), 0o755))
	require.NoError(t, os.WriteFile(stub, []byte("---\nname: legacy-only\n---\n"), 0o644))
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = se.repo, se.env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s: %s", args, out)
		return string(out)
	}
	git("add", "-f", "--", ".claude/agents/retired.md")
	git("add", "--", ".gitignore", "CLAUDE.md", ".claude/settings.json", ".punt-labs/ethos", ".punt-labs/ethos.yaml")
	git("-c", "core.hooksPath=/dev/null", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "Enable ethos with declared agent")
	require.Empty(t, git("status", "--porcelain"), "fixture must begin clean")

	execHookSessionStart(t, se)
	generated := filepath.Join(se.repo, ".claude", "agents", "worker.md")
	installed := filepath.Join(se.repo, ".claude", "agents", "legacy-only.md")
	assert.NoFileExists(t, generated, "startup must not generate agent definitions")
	assert.NoFileExists(t, installed, "startup must not install legacy stubs")
	require.Empty(t, git("status", "--porcelain"), "startup must not rewrite or prune tracked agents")
	_, stderr, code = runCLI(t, se, "enable", "--scope", "project", "--refresh")
	require.Zero(t, code, stderr)
	require.NoFileExists(t, stale, "explicit refresh must still prune retired agents")
	assert.FileExists(t, generated, "explicit refresh must generate the configured worker")
	assert.FileExists(t, installed, "explicit refresh must install the legacy stub")
}

func TestCLI_OffHooksSkipRepositoryWork(t *testing.T) {
	for _, deciding := range []string{"", ".punt-labs/local/ethos/disabled", ".punt-labs/ethos/opt-out"} {
		t.Run(deciding, func(t *testing.T) {
			se := freshCLIEnv(t, "refresh-human")
			if deciding != "" {
				_, stderr, code := runCLI(t, se, "enable", "--scope", "project")
				require.Zero(t, code, stderr)
				path := filepath.Join(se.repo, deciding)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, nil, 0o644))
			}
			stdout, stderr := execHookSessionStart(t, se)
			require.Empty(t, stdout)
			require.Empty(t, stderr)

			cmd := exec.Command(ethosBinary, "hook", "audit-log")
			cmd.Dir, cmd.Env = se.repo, se.env
			cmd.Stdin = strings.NewReader(`{"session_id":"refresh-cli-session","tool_name":"Bash","tool_input":{"command":"true"}}`)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			require.Empty(t, output)
			for _, dir := range []string{".punt-labs/local/ethos", ".punt-labs/ethos"} {
				files, err := filepath.Glob(filepath.Join(se.repo, dir, "**", "*refresh-cli-session*"))
				require.NoError(t, err)
				assert.Empty(t, files)
			}
		})
	}
}

func TestCLI_EnablementTogglesLeaveCleanCheckout(t *testing.T) {
	se := enableSubprocessRepo(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = se.repo, se.env
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s: %s", args, output)
		return string(output)
	}
	ethos := func(args ...string) {
		t.Helper()
		_, stderr, code := runCLI(t, se, args...)
		require.Zero(t, code, "%v: %s", args, stderr)
	}
	gitignore := filepath.Join(se.repo, ".gitignore")
	require.NoError(t, os.WriteFile(gitignore, []byte(".punt-labs/local/\nCLAUDE.local.md\n.claude/settings.local.json\n.*.punt-import.lock\n"), 0o644))
	ethos("enable", "--scope", "project")
	ethos("disable", "--scope", "project")
	git("add", "--all")
	git("commit", "-qm", "Shared integration disabled")
	for range 2 {
		ethos("enable")
		assert.Empty(t, git("status", "--porcelain"), "personal enable must not change tracked files")
		ethos("disable")
		assert.Empty(t, git("status", "--porcelain"), "personal disable must not change tracked files")
		ethos("enable", "--scope", "project")
		assert.NotEmpty(t, git("status", "--porcelain"), "shared enablement is a commit-visible change")
		ethos("disable", "--scope", "project")
		assert.Empty(t, git("status", "--porcelain"), "project toggle must restore the committed off state")
	}
	ethos("enable", "--scope", "project")
	git("add", "--all")
	git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "Shared integration enabled")
	ethos("enable", "--scope", "project")
	require.Empty(t, git("status", "--porcelain"), "repeated project enablement must not rewrite tracked files")
	ethos("disable")
	require.FileExists(t, filepath.Join(se.repo, ".punt-labs/local/ethos/disabled"))
	assert.Empty(t, git("status", "--porcelain"), "personal off must not remove tracked integration")
	ethos("enable")
	require.NoFileExists(t, filepath.Join(se.repo, ".punt-labs/local/ethos/disabled"))
	assert.Empty(t, git("status", "--porcelain"))
}

func TestDormantCheckoutKeepsPostEditDeadline(t *testing.T) {
	se := enableSubprocessRepo(t)
	t.Setenv("HOME", se.home)
	t.Chdir(se.repo)
	assert.True(t, dormantHook([]string{"hook", "post-edit"}))
	assert.False(t, dormantHook([]string{"hook", "stdin-watchdog", "123"}))
}

func TestCLI_EnablePinsDepositedGuideUntilRefresh(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	_, stderr, code := runCLI(t, se, "enable", "--scope", "project")
	require.Zero(t, code, stderr)
	guide := filepath.Join(se.repo, ".punt-labs", "ethos", "CLAUDE.md")
	pinned := []byte("guide deposited by another version\n")
	require.NoError(t, os.WriteFile(guide, pinned, 0o644))
	_, stderr, code = runCLI(t, se, "enable", "--scope", "project")
	require.Zero(t, code, stderr)
	after, err := os.ReadFile(guide)
	require.NoError(t, err)
	require.Equal(t, pinned, after)
	_, stderr, code = runCLI(t, se, "enable", "--scope", "project", "--refresh")
	require.Zero(t, code, stderr)
	after, err = os.ReadFile(guide)
	require.NoError(t, err)
	require.NotEqual(t, pinned, after)
}

func TestCLI_ProjectRefreshRollsBackWhenAgentInstallationFails(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	store := filepath.Join(se.repo, ".punt-labs", "ethos")
	writeRefreshTeam(t, se.repo, store)
	agents := filepath.Join(se.repo, ".claude", "agents")
	require.NoError(t, os.MkdirAll(agents, 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(agents, "worker.md"), 0o755))
	stubs := filepath.Join(store, "agents")
	require.NoError(t, os.MkdirAll(stubs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stubs, "helper.md"), []byte("---\nname: helper\n---\n"), 0o644))
	_, stderr, code := runCLI(t, se, "enable", "--scope", "project", "--refresh")
	require.NotZero(t, code, stderr)
	assert.NoFileExists(t, filepath.Join(store, "enabled"))
	assert.NoFileExists(t, filepath.Join(store, "CLAUDE.md"))
	assert.NoFileExists(t, filepath.Join(agents, "helper.md"), "an earlier stub installation must also roll back")
	assert.DirExists(t, filepath.Join(agents, "worker.md"))
}

func TestCLI_EnableAgentsOnlyLeavesDisabledRepoDisabled(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	store := filepath.Join(se.repo, ".punt-labs", "ethos")
	writeRefreshTeam(t, se.repo, store)
	guide := filepath.Join(store, "CLAUDE.md")
	const custom = "# Team-owned guide\n"
	require.NoError(t, os.WriteFile(guide, []byte(custom), 0o644))

	_, stderr, code := runCLI(t, se, "enable", "--agents-only")
	require.Zero(t, code, stderr)
	assert.FileExists(t, filepath.Join(se.repo, ".claude", "agents", "worker.md"))
	assert.NoFileExists(t, filepath.Join(store, "enabled"))
	assert.NoFileExists(t, filepath.Join(se.repo, "CLAUDE.md"))
	assert.NoFileExists(t, filepath.Join(se.repo, ".claude", "settings.json"))
	assert.NoFileExists(t, filepath.Join(se.repo, ".git", "hooks", "pre-commit"))
	after, err := os.ReadFile(guide)
	require.NoError(t, err)
	assert.Equal(t, custom, string(after))
}

func TestCLI_EnableRefreshGeneratesAgentsFromLegacyGitlink(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	store := filepath.Join(se.repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(store, 0o755))
	writeRefreshTeam(t, se.repo, store)
	require.NoError(t, os.WriteFile(filepath.Join(store, "CLAUDE.md"), []byte("# Team-owned guide\n"), 0o644))
	cmd := exec.Command("git", "update-index", "--add", "--cacheinfo",
		"160000,0000000000000000000000000000000000000001,.punt-labs/ethos")
	cmd.Dir, cmd.Env = se.repo, se.env
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)

	stdout, stderr, code := runCLI(t, se, "enable", "--scope", "project", "--refresh")
	require.Zero(t, code, stderr)
	assert.Contains(t, stdout, "refreshed Claude agent definitions")
	assert.Contains(t, stderr, "convert it to an inline directory")
	generated, err := os.ReadFile(filepath.Join(se.repo, ".claude", "agents", "worker.md"))
	require.NoError(t, err, stderr)
	assert.Contains(t, string(generated), "Legacy Worker")
	guide, err := os.ReadFile(filepath.Join(store, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Team-owned guide\n", string(guide))
	assert.NoFileExists(t, filepath.Join(store, "enabled"))
	assert.NoFileExists(t, filepath.Join(se.repo, "CLAUDE.md"))
	assert.NoFileExists(t, filepath.Join(se.repo, ".claude", "settings.json"))
}

func writeRefreshTeam(t *testing.T, repo, store string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), []byte("team: legacy\n"), 0o644))
	for path, body := range map[string]string{
		"teams/legacy.yaml":       "name: legacy\nmembers:\n  - identity: worker\n    role: reviewer\n",
		"identities/worker.yaml":  "name: Legacy Worker\nhandle: worker\nkind: agent\npersonality: worker\n",
		"personalities/worker.md": "# Legacy Worker\n\nReview the implementation.\n",
		"roles/reviewer.yaml":     "name: reviewer\ntools: [Read]\n",
	} {
		p := filepath.Join(store, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
}

// A stale seal section in an enabled repo is rewritten by SessionStart, and
// the session is told; a dormant repo and a current section are left alone.
func TestCLI_SessionStartRefreshesStaleHook(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the current CLI binary must build; this acceptance must not skip")
	stale := "#!/bin/sh\n" +
		"# --- BEGIN " + hooks.SealTag + " ---\n" +
		"# " + hooks.SealIdent + "\n" +
		"ethos audit seal || exit 2\n" +
		"# --- END " + hooks.SealTag + " ---\n"

	t.Run("enabled repo", func(t *testing.T) {
		se := freshCLIEnv(t, "refresh-human")
		_, stderr, code := runCLI(t, se, "enable", "--scope", "project")
		require.Equal(t, 0, code, stderr)
		hookPath := filepath.Join(se.repo, ".git", "hooks", "pre-commit")
		current, err := os.ReadFile(hookPath)
		require.NoError(t, err)

		out, errOut := execHookSessionStart(t, se)
		assert.NotContains(t, errOut, "refreshed stale git hook", "a current section is a no-op")
		assert.NotContains(t, out, "refreshed stale git hook")

		require.NoError(t, os.WriteFile(hookPath, []byte(stale), 0o755))
		out, errOut = execHookSessionStart(t, se)
		assert.Contains(t, errOut, "ethos: session-start: refreshed stale git hook section(s) at session start: pre-commit")
		assert.Contains(t, out, "refreshed stale git hook section(s) at session start: pre-commit")
		after, err := os.ReadFile(hookPath)
		require.NoError(t, err)
		assert.Equal(t, string(current), string(after))
	})

	t.Run("dormant repo", func(t *testing.T) {
		se := freshCLIEnv(t, "refresh-human")
		hooksDir := filepath.Join(se.repo, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooksDir, 0o755))
		hookPath := filepath.Join(hooksDir, "pre-commit")
		require.NoError(t, os.WriteFile(hookPath, []byte(stale), 0o755))

		_, errOut := execHookSessionStart(t, se)
		assert.NotContains(t, errOut, "refreshed stale git hook")
		after, err := os.ReadFile(hookPath)
		require.NoError(t, err)
		assert.Equal(t, stale, string(after))
	})
}
