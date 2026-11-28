//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// enableSubprocessRepo builds a git repo and an isolated HOME under /tmp, never
// under this repo's TMPDIR (which .envrc points inside the working tree). The
// built binary resolves the repo root and deposits into it; keeping the fixture
// in /tmp guarantees a resolution escape cannot contaminate this repo. The
// isolated HOME keeps the seal's global reads/writes off the real home.
func enableSubprocessRepo(t *testing.T) *cliSubprocessEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home, err := os.MkdirTemp("/tmp", "ethos-en-home-*")
	if err != nil {
		t.Skipf("cannot create an outside-repo temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	repo, err := os.MkdirTemp("/tmp", "ethos-en-repo-*")
	if err != nil {
		t.Skipf("cannot create an outside-repo temp repo: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(repo) })

	env := []string{
		"HOME=" + home,
		"USER=test-agent",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	gitRunEnv(t, repo, env, "init", "-q")
	gitRunEnv(t, repo, env, "config", "user.email", "test@example.com")
	gitRunEnv(t, repo, env, "config", "user.name", "test")
	return &cliSubprocessEnv{home: home, repo: repo, env: testhelpers.CoverEnv(env)}
}

func gitRunEnv(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := testhelpers.GitCommand(append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestCLI_EnableHelpListsRefreshModes(t *testing.T) {
	se := enableSubprocessRepo(t)
	stdout, stderr, code := runCLI(t, se, "--help")
	if code != 0 {
		t.Fatalf("help exited %d: %s", code, stderr)
	}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "enable" {
			continue
		}
		if !strings.Contains(line, "--refresh") || !strings.Contains(line, "--agents-only") {
			t.Fatalf("enable summary omits a refresh mode: %s", line)
		}
		return
	}
	t.Fatalf("enable summary missing from help:\n%s", stdout)
}

func TestInstallerLeavesInvokingRepositoryUntouched(t *testing.T) {
	se := enableSubprocessRepo(t)
	installer, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", installer, "--no-plugin")
	cmd.Dir = se.repo
	cmd.Env = append(se.env, "ETHOS_LOCAL_BINARY="+ethosBinary)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}
	git := exec.Command("git", "status", "--porcelain")
	git.Dir, git.Env = se.repo, se.env
	if output, err := git.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("install changed the invoking repository: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(se.repo, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Fatalf("install unexpectedly installed a repository hook: %v", err)
	}
}

func TestCLI_EnableSuccess(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)
	stdout, stderr, code := runCLI(t, se, "enable", "--scope", "project")
	if code != 0 {
		t.Fatalf("enable exit = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "ethos enabled in") {
		t.Errorf("stdout = %q, want 'ethos enabled in'", stdout)
	}
	for _, rel := range []string{
		filepath.Join(".punt-labs", "ethos", "CLAUDE.md"),
		filepath.Join(".punt-labs", "ethos", ".vendored-manifest"),
		filepath.Join(".punt-labs", "ethos", "enabled"),
		"CLAUDE.md",
	} {
		if _, err := os.Stat(filepath.Join(se.repo, rel)); err != nil {
			t.Errorf("artifact %s missing after enable: %v", rel, err)
		}
	}
	// No repo config in the fixture, so enable prints the setup hint.
	if !strings.Contains(stdout, "setup") {
		t.Errorf("stdout = %q, want a 'run ethos setup' hint", stdout)
	}
}

func TestCLI_EnableDefaultsToPersonalScope(t *testing.T) {
	se := enableSubprocessRepo(t)
	ignores := ".punt-labs/local/\nCLAUDE.local.md\n.claude/settings.local.json\n"
	if err := os.WriteFile(filepath.Join(se.repo, ".gitignore"), []byte(ignores), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, se, "enable")
	if code != 0 {
		t.Fatalf("enable failed: %s", stderr)
	}
	for _, rel := range []string{".punt-labs/local/ethos/enabled", ".punt-labs/local/ethos/CLAUDE.md", "CLAUDE.local.md", ".claude/settings.local.json"} {
		if _, err := os.Stat(filepath.Join(se.repo, rel)); err != nil {
			t.Errorf("personal enable is missing %s: %v", rel, err)
		}
	}
	for _, rel := range []string{".punt-labs/ethos/enabled", "CLAUDE.md", ".claude/settings.json", ".git/hooks/pre-commit"} {
		if _, err := os.Lstat(filepath.Join(se.repo, rel)); !os.IsNotExist(err) {
			t.Errorf("personal enable touched %s: %v", rel, err)
		}
	}
	_, stderr, code = runCLI(t, se, "disable")
	if code != 0 {
		t.Fatalf("disable failed: %s", stderr)
	}
	if _, err := os.Lstat(filepath.Join(se.repo, ".punt-labs/local/ethos/enabled")); !os.IsNotExist(err) {
		t.Errorf("personal disable left its marker: %v", err)
	}
}

func TestCLI_EnableRefusesOptOutBeforeRefreshingAgents(t *testing.T) {
	se := enableSubprocessRepo(t)
	const marker = ".punt-labs/ethos/opt-out"
	path := filepath.Join(se.repo, marker)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, se, "enable", "--agents-only")
	if code == 0 || !strings.Contains(stderr, marker) {
		t.Fatalf("enable --agents-only exit = %d; stderr = %q; want refusal naming %s", code, stderr, marker)
	}
	if _, err := os.Stat(filepath.Join(se.repo, ".punt-labs", "local")); !os.IsNotExist(err) {
		t.Fatalf("refused enable changed the runtime zone: %v", err)
	}
}

func TestCLI_ProjectOptOutRoundTrip(t *testing.T) {
	se := enableSubprocessRepo(t)
	_, stderr, code := runCLI(t, se, "enable", "--scope", "project")
	if code != 0 {
		t.Fatalf("project enable: %s", stderr)
	}
	_, stderr, code = runCLI(t, se, "disable", "--scope", "project", "--opt-out")
	if code != 0 {
		t.Fatalf("project opt-out: %s", stderr)
	}
	optOut := filepath.Join(se.repo, ".punt-labs", "ethos", "opt-out")
	if _, err := os.Stat(optOut); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(se.repo, "CLAUDE.md"), []byte("```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code = runCLI(t, se, "enable", "--scope", "project", "--clear-opt-out"); code == 0 {
		t.Fatalf("clear and enable accepted a broken import host: %s", stderr)
	}
	if _, err := os.Stat(optOut); err != nil {
		t.Fatalf("failed enable lost opt-out: %v", err)
	}
	if err := os.Remove(filepath.Join(se.repo, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runCLI(t, se, "enable", "--scope", "project", "--clear-opt-out")
	if code != 0 {
		t.Fatalf("clear opt-out: %s", stderr)
	}
	if _, err := os.Lstat(optOut); !os.IsNotExist(err) {
		t.Fatalf("opt-out remained after successful clear: %v", err)
	}
}

func TestCLI_EnableJSONShape(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)
	stdout, stderr, code := runCLI(t, se, "enable", "--scope", "project", "--json")
	if code != 0 {
		t.Fatalf("enable --json exit = %d, want 0; stderr: %s", code, stderr)
	}
	var rep struct {
		RepoRoot string `json:"repo_root"`
		Steps    []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"steps"`
		Hint string `json:"hint"`
	}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("enable --json is not valid JSON: %v\n%s", err, stdout)
	}
	if rep.RepoRoot == "" {
		t.Error("repo_root empty in --json report")
	}
	if len(rep.Steps) == 0 {
		t.Fatal("no steps in --json report")
	}
	// Every step must name itself and carry a status — the machine contract.
	for i, s := range rep.Steps {
		if s.Step == "" {
			t.Errorf("step[%d] has an empty step name", i)
		}
		if s.Status == "" {
			t.Errorf("step[%d] (%s) has an empty status", i, s.Step)
		}
	}
}

func TestCLI_EnableIdempotent(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)
	if _, stderr, code := runCLI(t, se, "enable", "--scope", "project"); code != 0 {
		t.Fatalf("first enable exit = %d; stderr: %s", code, stderr)
	}
	if _, stderr, code := runCLI(t, se, "enable", "--scope", "project"); code != 0 {
		t.Fatalf("second enable exit = %d; stderr: %s", code, stderr)
	}
	claude, err := os.ReadFile(filepath.Join(se.repo, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(claude), "@.punt-labs/ethos/CLAUDE.md"); n != 1 {
		t.Errorf("import line count = %d after two enables, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(se.repo, ".punt-labs", "ethos", "enabled")); err != nil {
		t.Errorf("marker missing after idempotent re-enable: %v", err)
	}
}

func TestCLI_DisableRoundTrip(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)
	if _, stderr, code := runCLI(t, se, "enable", "--scope", "project"); code != 0 {
		t.Fatalf("enable exit = %d; stderr: %s", code, stderr)
	}
	stdout, stderr, code := runCLI(t, se, "disable", "--scope", "project")
	if code != 0 {
		t.Fatalf("disable exit = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "ethos disabled in") {
		t.Errorf("stdout = %q, want 'ethos disabled in'", stdout)
	}
	if _, err := os.Stat(filepath.Join(se.repo, ".punt-labs", "ethos", "enabled")); !os.IsNotExist(err) {
		t.Error("marker survived disable")
	}
	claude, err := os.ReadFile(filepath.Join(se.repo, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(claude), "@.punt-labs/ethos/CLAUDE.md") {
		t.Error("import line survived disable")
	}
	// The guide stays dormant on disk (non-destructive disable).
	if _, err := os.Stat(filepath.Join(se.repo, ".punt-labs", "ethos", "CLAUDE.md")); err != nil {
		t.Errorf("guide removed by disable (should stay dormant): %v", err)
	}
}

func TestCLI_EnableNotInRepoExit2(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	dir := testhelpers.OutsideRepo(t)
	// Exercise the child environment too: omitting its ceiling would expose
	// this test-owned broken ancestor rather than the intended outside state.
	if err := os.Mkdir(filepath.Join(filepath.Dir(dir), ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	se := &cliSubprocessEnv{
		home: dir,
		repo: dir,
		env: []string{
			"HOME=" + dir,
			"GIT_CEILING_DIRECTORIES=" + os.Getenv("GIT_CEILING_DIRECTORIES"),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"PATH=" + os.Getenv("PATH"),
		},
	}
	_, stderr, code := runCLI(t, se, "enable")
	if code != 2 {
		t.Fatalf("enable outside a repo exit = %d, want 2; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "not in a git repository") {
		t.Errorf("stderr = %q, want 'not in a git repository'", stderr)
	}
}

func TestCLI_EnableGitlinkGuardExit1(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)
	// Record .punt-labs/ethos as a gitlink (mode 160000) in the index.
	cmd := testhelpers.GitCommand("-C", se.repo, "update-index", "--add",
		"--cacheinfo", "160000,0000000000000000000000000000000000000001,.punt-labs/ethos")
	cmd.Env = se.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot stage a gitlink in this git: %v: %s", err, out)
	}
	_, stderr, code := runCLI(t, se, "enable", "--scope", "project")
	if code != 1 {
		t.Fatalf("enable on a gitlink exit = %d, want 1; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "ethos-e29s") {
		t.Errorf("stderr = %q, want the vendor-first remedy ethos-e29s", stderr)
	}
}

func TestCLI_EnableOpenFenceHostExit1(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)
	const body = "# title\n\n```sh\necho hi\n" // fence opened, never closed
	if err := os.WriteFile(filepath.Join(se.repo, "CLAUDE.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, se, "enable", "--scope", "project")
	if code != 1 {
		t.Fatalf("enable on an open-fence host exit = %d, want 1; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "unclosed code fence") {
		t.Errorf("stderr = %q, want 'unclosed code fence'", stderr)
	}
	got, err := os.ReadFile(filepath.Join(se.repo, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("host CLAUDE.md changed on the error path: got %q", got)
	}
}

func TestCLI_DisableWorktreeRefusal(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)
	// A commit is required before a worktree can be added.
	if err := os.WriteFile(filepath.Join(se.repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunEnv(t, se.repo, se.env, "add", "f")
	gitRunEnv(t, se.repo, se.env, "commit", "-q", "-m", "init")
	wtParent, err := os.MkdirTemp("/tmp", "ethos-en-wt-*")
	if err != nil {
		t.Skipf("cannot create a worktree parent: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wtParent) })
	wt := filepath.Join(wtParent, "wt")
	gitRunEnv(t, se.repo, se.env, "worktree", "add", "-q", wt)

	if _, stderr, code := runCLI(t, se, "enable", "--scope", "project"); code != 0 {
		t.Fatalf("enable main exit = %d; stderr: %s", code, stderr)
	}
	wtEnv := &cliSubprocessEnv{home: se.home, repo: wt, env: se.env}
	if _, stderr, code := runCLI(t, wtEnv, "enable", "--scope", "project"); code != 0 {
		t.Fatalf("enable worktree exit = %d; stderr: %s", code, stderr)
	}

	// Disabling the main checkout must refuse: the sibling worktree is enabled
	// and the git hooks are shared across worktrees.
	_, stderr, code := runCLI(t, se, "disable", "--scope", "project")
	if code != 1 {
		t.Fatalf("disable with an enabled sibling exit = %d, want 1; stderr: %s", code, stderr)
	}
	// --force overrides the refusal.
	if _, stderr, code := runCLI(t, se, "disable", "--scope", "project", "--force"); code != 0 {
		t.Fatalf("disable --force exit = %d, want 0; stderr: %s", code, stderr)
	}
}

func TestCLI_DisableForceWithoutScopeRemovesSharedIntegration(t *testing.T) {
	se := enableSubprocessRepo(t)
	_, stderr, code := runCLI(t, se, "enable", "--scope", "project")
	require.Zero(t, code, stderr)
	marker := filepath.Join(se.repo, ".punt-labs", "ethos", "enabled")
	require.FileExists(t, marker)
	_, stderr, code = runCLI(t, se, "disable", "--force")
	require.Zero(t, code, stderr)
	require.NoFileExists(t, marker)
	require.NoFileExists(t, filepath.Join(se.repo, ".git", "hooks", "pre-commit"))
}
