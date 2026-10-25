//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func emptyGitFile(dot string) error { return os.WriteFile(dot, nil, 0o644) }

// nestedGitEnv is an outer repository holding a store and an inner directory
// whose .git is built by mk.
func nestedGitEnv(t *testing.T, mk func(dot string) error) (se *cliSubprocessEnv, inner string) {
	t.Helper()
	se = setupCLISubprocessEnv(t)
	inner = filepath.Join(se.repo, "inner")
	require.NoError(t, os.Mkdir(inner, 0o755))
	if err := mk(filepath.Join(inner, ".git")); err != nil {
		t.Skipf("making .git: %v", err)
	}
	return se, inner
}

func createIdentityFrom(t *testing.T, se *cliSubprocessEnv, dir string) (string, int) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "id.yaml")
	require.NoError(t, os.WriteFile(file, []byte("name: Nested\nhandle: nested\nkind: agent\n"), 0o644))
	cmd := exec.Command(ethosBinary, "identity", "create", "--file", file)
	cmd.Dir = dir
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else {
		require.NoError(t, err)
	}
	return string(out), code
}

// A .git that git skips (a dangling symlink, an empty directory, a symlink
// loop) must not divert a write to the global store: git treats inner as part of
// outer, and so does ethos.
func TestCLI_SkippedInnerGitUsesOuterStore(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	cases := map[string]func(dot string) error{
		"dangling": func(dot string) error { return os.Symlink(filepath.Join(filepath.Dir(dot), "absent"), dot) },
		"empty":    func(dot string) error { return os.Mkdir(dot, 0o755) },
		"loop":     func(dot string) error { return os.Symlink(dot, dot) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			se, inner := nestedGitEnv(t, mk)
			out, code := createIdentityFrom(t, se, inner)
			require.Equal(t, 0, code, out)
			assert.FileExists(t, filepath.Join(se.repo, ".punt-labs", "ethos", "identities", "nested.yaml"))
			assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "identities", "nested.yaml"))
		})
	}
}

// A .git git refuses (an empty or garbage gitfile, a gitdir to nowhere) is a
// refusal: git stops there, so ethos must not write to the outer or global store.
func TestCLI_InvalidInnerGitRefuses(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	cases := map[string]func(dot string) error{
		"empty":   emptyGitFile,
		"garbage": func(dot string) error { return os.WriteFile(dot, []byte("not a gitfile\n"), 0o644) },
		"nowhere": func(dot string) error { return os.WriteFile(dot, []byte("gitdir: /nonexistent\n"), 0o644) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			se, inner := nestedGitEnv(t, mk)
			out, code := createIdentityFrom(t, se, inner)
			assert.NotEqual(t, 0, code, out)
			assert.Contains(t, out, filepath.Join(inner, ".git"))
			assert.NoFileExists(t, filepath.Join(se.repo, ".punt-labs", "ethos", "identities", "nested.yaml"))
			assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "identities", "nested.yaml"))
		})
	}
}

// A .git git refuses is a refusal, never a silent global write.
func TestCLI_UnreadableGitRefusesWrites(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	out, code := createIdentityFrom(t, se, inner)
	assert.NotEqual(t, 0, code, out)
	assert.Contains(t, out, filepath.Join(inner, ".git"))
	assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "identities", "nested.yaml"))
	assert.NoFileExists(t, filepath.Join(se.repo, ".punt-labs", "ethos", "identities", "nested.yaml"))
}

// A command that reports on the degraded state stays runnable, and exits
// non-zero with a FAIL row: every non-exempt command would refuse here.
func TestCLI_DoctorRunsOnUnreadableGit(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	cmd := exec.Command(ethosBinary, "doctor")
	cmd.Dir = inner
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	out, err := cmd.CombinedOutput()
	assert.Contains(t, string(out), "Identity directory", "doctor still prints its table")
	assert.Contains(t, string(out), "Repository store")
	assert.Contains(t, string(out), "not located: ")
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, string(out))
	assert.NotEqual(t, 0, ee.ExitCode())
}

// Every command that reads or writes the store, plus seed, refuses on an
// unreadable .git instead of using the global store. whoami, session start,
// and adr create (its author) resolve through the repository identity store,
// so they refuse too.
func TestCLI_UnreadableGitRefusesStoreCommands(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	for _, args := range [][]string{
		{"seed"},
		{"vendor"},
		{"find", "missions"},
		{"team", "activate", "x"},
		{"role", "create", "x"},
		{"mission", "list"},
		{"identity", "list"},
		{"whoami"},
		{"identity", "whoami"},
		{"session", "start"},
		{"adr", "create", "--title", "t", "--decision", "d"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.Command(ethosBinary, args...)
			cmd.Dir = inner
			cmd.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, string(out))
			assert.Contains(t, string(out), filepath.Join(inner, ".git"))
			assert.NotContains(t, string(out), "ethos: ethos", "the command path is not prefixed twice")
		})
	}
}

// The commands that touch only global stores run on an unreadable .git: none
// of them refuses on the repository store.
func TestCLI_GlobalOnlyCommandsRunOnUnreadableGit(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se, inner := nestedGitEnv(t, emptyGitFile)
	for _, args := range [][]string{
		{"session"},
		{"session", "list"},
		{"session", "show"},
		{"session", "roster"},
		{"session", "write-current", "--pid", "1", "--session", "s"},
		{"session", "purge"},
		{"session", "end"},
		{"session", "join", "--agent-id", "a", "--session", "s"},
		{"session", "leave", "--agent-id", "a", "--session", "s"},
		{"session", "create", "--session", "s", "--root-id", "r", "--primary-id", "p"},
		{"session", "delete", "--session", "s"},
		{"session", "delete-current", "--pid", "1"},
		{"iam", "x"},
		{"session", "iam", "x"},
		{"adr", "list"},
		{"adr", "show", "x"},
		{"adr", "settle", "x"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.Command(ethosBinary, args...)
			cmd.Dir = inner
			cmd.Env = withForcedNotUnderClaudeCode(se.env)
			out, _ := cmd.CombinedOutput()
			assert.NotContains(t, string(out), "refusing to use the global store", string(out))
		})
	}
}

// Every command is classified for the store refusal, so a new command cannot
// run unclassified: the table names exactly the commands cobra has.
func TestEveryCommandClassifiedForStoreRefusal(t *testing.T) {
	// cobra adds its help command when the root first executes; add it now
	// so the walk is the same whichever tests ran before.
	rootCmd.InitDefaultHelpCmd()
	seen := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		path := c.CommandPath()
		seen[path] = true
		_, ok := storeUses[path]
		assert.True(t, ok || slices.Contains(cobraCommands, path), "%s is not classified in storeUses", path)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	for path := range storeUses {
		assert.True(t, seen[path], "storeUses names %q, which is not a command", path)
	}
}

// The classes the rulings fixed, and the default for anything unclassified.
func TestStoreRefusalExemptions(t *testing.T) {
	cases := map[storeUse][][]string{
		storeHandlesError: {{"doctor"}, {"setup"}, {"seed"}, {"enable"}, {"disable"},
			{"serve"}, {"hook"}, {"hook", "audit-log"}, {"audit", "seal"}},
		storeGlobalOnly: {{"version"}, {"uninstall"}, {"completion"}, {"role", "schema"},
			{"session"}, {"session", "list"}, {"session", "show"}, {"session", "roster"},
			{"session", "write-current"}, {"session", "purge"}, {"session", "end"},
			{"session", "join"}, {"session", "leave"}, {"session", "create"},
			{"session", "delete"}, {"session", "delete-current"},
			{"iam"}, {"session", "iam"},
			{"adr", "list"}, {"adr", "show"}, {"adr", "settle"}},
		storeRefuses: {{"identity", "list"}, {"mission", "list"},
			{"whoami"}, {"identity", "whoami"},
			{"session", "start"}, {"adr", "create"}},
	}
	for want, cmds := range cases {
		for _, args := range cmds {
			c, _, err := rootCmd.Find(args)
			require.NoError(t, err, args)
			assert.Equal(t, want, storeUses[c.CommandPath()], "%v", args)
			assert.Equal(t, want != storeRefuses, exemptFromStoreRefusal(c), "%v", args)
		}
	}
	unclassified := &cobra.Command{Use: "brand-new"}
	rootCmd.AddCommand(unclassified)
	t.Cleanup(func() { rootCmd.RemoveCommand(unclassified) })
	assert.False(t, exemptFromStoreRefusal(unclassified), "an unclassified command refuses")
}

// End to end: a linked worktree of a repository whose config cannot be read
// has a store ethos cannot decide the location of. A store command refuses,
// naming the file, and never uses the global store.
func TestCLI_UnreadableRepositoryConfigRefusesStoreCommands(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	se := setupCLISubprocessEnv(t)
	for _, args := range [][]string{
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := testhelpers.GitCommand(append([]string{"-C", se.repo}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	wt := filepath.Join(t.TempDir(), "wt")
	cmd := testhelpers.GitCommand("-C", se.repo, "worktree", "add", "-q", wt)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	cfg := filepath.Join(se.repo, ".git", "config")
	require.NoError(t, os.Chmod(cfg, 0o000))
	t.Cleanup(func() { _ = os.Chmod(cfg, 0o644) })

	c := exec.Command(ethosBinary, "identity", "list")
	c.Dir = wt
	c.Env = withForcedNotUnderClaudeCode(se.env)
	combined, err := c.CombinedOutput()
	require.Error(t, err, string(combined))
	assert.Contains(t, string(combined), cfg)
	assert.Contains(t, string(combined), "refusing")
}

// treeFiles lists every file under root, relative, for before/after checks.
func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, rel)
		}
		return nil
	}))
	return files
}

// A .punt-labs/ethos that is a dangling symlink is a store ethos cannot read.
// A store command refuses (exit 2) rather than create the identity or talent
// in the global store; nothing is written under HOME.
func TestCLI_DanglingEthosStoreRefusesWrites(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	store := filepath.Join(se.repo, ".punt-labs", "ethos")
	require.NoError(t, os.RemoveAll(store))
	if err := os.Symlink(filepath.Join(se.repo, "nowhere"), store); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := treeFiles(t, se.home)
	content := filepath.Join(t.TempDir(), "t.md")
	require.NoError(t, os.WriteFile(content, []byte("# t\n"), 0o644))
	idFile := filepath.Join(t.TempDir(), "id.yaml")
	require.NoError(t, os.WriteFile(idFile, []byte("name: N\nhandle: n\nkind: agent\n"), 0o644))

	for name, args := range map[string][]string{
		"talent create":   {"talent", "create", "go", "-f", content},
		"identity create": {"identity", "create", "--file", idFile},
	} {
		t.Run(name, func(t *testing.T) {
			c := exec.Command(ethosBinary, args...)
			c.Dir = se.repo
			c.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := c.CombinedOutput()
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, string(out))
			assert.Equal(t, 2, ee.ExitCode(), string(out))
			assert.Contains(t, string(out), store)
		})
	}
	assert.Equal(t, before, treeFiles(t, se.home), "nothing is written under HOME")
}

// A repo config that cannot be read is an error for the leader lookup, not
// "claude": scaffold, dispatch and correct refuse rather than write a contract
// or a correction under the wrong author.
func TestCLI_UnreadableRepoConfigRefusesLeaderCommands(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), []byte("agent: [unclosed\n"), 0o644))
	for name, args := range map[string][]string{
		"scaffold": {"mission", "scaffold", "contract"},
		"dispatch": {"mission", "dispatch", "--worker", "bwk", "--evaluator", "djb", "--write-set", "a.go", "--criteria", "c"},
	} {
		t.Run(name, func(t *testing.T) {
			c := exec.Command(ethosBinary, args...)
			c.Dir = se.repo
			c.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := c.CombinedOutput()
			require.Error(t, err, string(out))
			assert.Contains(t, string(out), "repo config")
			assert.NotContains(t, string(out), "leader: claude", "no contract is written under a guessed leader")
		})
	}
}
