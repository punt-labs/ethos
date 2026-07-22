//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	return &cliSubprocessEnv{home: home, repo: repo, env: env}
}

func gitRunEnv(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestCLI_EnableSuccess(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)
	stdout, stderr, code := runCLI(t, se, "enable")
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

func TestCLI_EnableJSONShape(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)
	stdout, stderr, code := runCLI(t, se, "enable", "--json")
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
	if _, stderr, code := runCLI(t, se, "enable"); code != 0 {
		t.Fatalf("first enable exit = %d; stderr: %s", code, stderr)
	}
	if _, stderr, code := runCLI(t, se, "enable"); code != 0 {
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
	if _, stderr, code := runCLI(t, se, "enable"); code != 0 {
		t.Fatalf("enable exit = %d; stderr: %s", code, stderr)
	}
	stdout, stderr, code := runCLI(t, se, "disable")
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
	dir, err := os.MkdirTemp("/tmp", "ethos-en-norepo-*")
	if err != nil {
		t.Skipf("cannot create an outside-repo temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	se := &cliSubprocessEnv{
		home: dir,
		repo: dir,
		env: []string{
			"HOME=" + dir,
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
	cmd := exec.Command("git", "-C", se.repo, "update-index", "--add",
		"--cacheinfo", "160000,0000000000000000000000000000000000000001,.punt-labs/ethos")
	cmd.Env = se.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot stage a gitlink in this git: %v: %s", err, out)
	}
	_, stderr, code := runCLI(t, se, "enable")
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
	_, stderr, code := runCLI(t, se, "enable")
	if code != 1 {
		t.Fatalf("enable on an open-fence host exit = %d, want 1; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "unterminated code fence") {
		t.Errorf("stderr = %q, want 'unterminated code fence'", stderr)
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

	if _, stderr, code := runCLI(t, se, "enable"); code != 0 {
		t.Fatalf("enable main exit = %d; stderr: %s", code, stderr)
	}
	wtEnv := &cliSubprocessEnv{home: se.home, repo: wt, env: se.env}
	if _, stderr, code := runCLI(t, wtEnv, "enable"); code != 0 {
		t.Fatalf("enable worktree exit = %d; stderr: %s", code, stderr)
	}

	// Disabling the main checkout must refuse: the sibling worktree is enabled
	// and the git hooks are shared across worktrees.
	_, stderr, code := runCLI(t, se, "disable")
	if code != 1 {
		t.Fatalf("disable with an enabled sibling exit = %d, want 1; stderr: %s", code, stderr)
	}
	// --force overrides the refusal.
	if _, stderr, code := runCLI(t, se, "disable", "--force"); code != 0 {
		t.Fatalf("disable --force exit = %d, want 0; stderr: %s", code, stderr)
	}
}
