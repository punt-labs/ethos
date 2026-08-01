package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/enable"
)

// A repo that already excludes machine-local files must come back from setup
// and vendor --apply untouched, whatever spelling it uses. String-matching one
// blessed rule re-appended a narrower near-duplicate on every run and dirtied
// the tree (Bugbot, PR #422). "Already excludes" means all of them: a rule that
// covers only part of the namespace is not on this list — see
// TestEnsureLocalExtIgnoredUpgradesNarrowRule.
func TestEnsureLocalExtIgnoredLeavesCoveredRepoAlone(t *testing.T) {
	cases := []struct {
		name      string
		gitignore string
	}{
		{"canonical rule", enable.LocalIgnoreRule + "\n"},
		{"whole subtree", ".punt-labs/**\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := enableGitRepo(t)
			path := filepath.Join(dir, ".gitignore")
			if err := os.WriteFile(path, []byte(c.gitignore), 0o644); err != nil {
				t.Fatal(err)
			}
			added, err := ensureLocalExtIgnored(dir)
			if err != nil {
				t.Fatalf("ensureLocalExtIgnored: %v", err)
			}
			if added {
				t.Error("added a rule to a repo that already excludes .local files")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.gitignore {
				t.Errorf(".gitignore rewritten:\nwant:\n%s\ngot:\n%s", c.gitignore, got)
			}
		})
	}
}

func TestEnsureLocalExtIgnoredAddsCanonicalRuleOnce(t *testing.T) {
	dir := enableGitRepo(t)
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	added, err := ensureLocalExtIgnored(dir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !added {
		t.Fatal("no rule added to an unprotected repo")
	}
	got := readGitignore(t, path)
	if n := strings.Count(got, enable.LocalIgnoreRule); n != 1 {
		t.Errorf("canonical rule appears %d times, want 1; got:\n%s", n, got)
	}
	if strings.Contains(got, ".punt-labs/ethos/**/*.local.yaml") {
		t.Errorf("wrote the narrow rule instead of the canonical one; got:\n%s", got)
	}
	if !strings.HasPrefix(got, "*.log\n") {
		t.Errorf("existing content not preserved; got:\n%s", got)
	}

	// The second call is what setup and vendor --apply do on every re-run.
	added, err = ensureLocalExtIgnored(dir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if added {
		t.Error("rule appended twice")
	}
	if again := readGitignore(t, path); again != got {
		t.Errorf(".gitignore changed on re-run:\nwant:\n%s\ngot:\n%s", got, again)
	}
}

// The narrow rule every shipped `ethos doctor` told operators to add,
// `.punt-labs/ethos/**/*.local.yaml`, covers one corner of the namespace and
// leaves `.punt-labs/vox/`, `.punt-labs/beadle/`, and every non-yaml variant
// stageable. One probe under .punt-labs/ethos/ read it as coverage, so ethos
// wrote nothing and `git add -A` staged the secret (djb, review of PR #423).
// It must now read as uncovered and get the canonical rule alongside it.
func TestEnsureLocalExtIgnoredUpgradesNarrowRule(t *testing.T) {
	const narrow = ".punt-labs/ethos/**/*.local.yaml\n"
	dir := enableGitRepo(t)
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte(narrow), 0o644); err != nil {
		t.Fatal(err)
	}

	added, err := ensureLocalExtIgnored(dir)
	if err != nil {
		t.Fatalf("ensureLocalExtIgnored: %v", err)
	}
	if !added {
		t.Fatal("narrow rule accepted as coverage; the canonical rule was not written")
	}
	got := readGitignore(t, path)
	if !strings.HasPrefix(got, narrow) {
		t.Errorf("existing rule not preserved; got:\n%s", got)
	}
	if n := strings.Count(got, enable.LocalIgnoreRule); n != 1 {
		t.Errorf("canonical rule appears %d times, want 1; got:\n%s", n, got)
	}
	covered, err := enable.LocalIgnored(dir)
	if err != nil {
		t.Fatalf("LocalIgnored: %v", err)
	}
	if !covered {
		t.Errorf("repo still uncovered after writing the rule; .gitignore:\n%s", got)
	}

	// djb's repro. The secrets the narrow rule missed must not reach the index.
	for _, f := range []string{".punt-labs/vox/vox.local.md", ".punt-labs/beadle/creds.local.json"} {
		writeRepoFile(t, dir, f, "secret\n")
	}
	if staged := gitAddAll(t, dir); len(staged) != 1 || staged[0] != ".gitignore" {
		t.Errorf("git add -A staged %v, want only [.gitignore]", staged)
	}
}

// A repo with no .gitignore at all gets one, and the rule it gets must be the
// rule git honours — the same question `ethos doctor` asks.
func TestEnsureLocalExtIgnoredCreatesGitignore(t *testing.T) {
	dir := enableGitRepo(t)
	added, err := ensureLocalExtIgnored(dir)
	if err != nil {
		t.Fatalf("ensureLocalExtIgnored: %v", err)
	}
	if !added {
		t.Fatal("no rule added to a repo with no .gitignore")
	}
	got := readGitignore(t, filepath.Join(dir, ".gitignore"))
	if !strings.Contains(got, enable.LocalIgnoreNote) {
		t.Errorf("rule written without its explanation; got:\n%s", got)
	}
	covered, err := enable.LocalIgnored(dir)
	if err != nil {
		t.Fatalf("LocalIgnored: %v", err)
	}
	if !covered {
		t.Errorf("git does not honour the written rule; .gitignore:\n%s", got)
	}
}

func TestEnsureLocalExtIgnoredOutsideRepo(t *testing.T) {
	added, err := ensureLocalExtIgnored("")
	if err != nil {
		t.Fatalf("ensureLocalExtIgnored: %v", err)
	}
	if added {
		t.Error("wrote a .gitignore with no repo root")
	}
}

// writeRepoFile creates the repo-relative file rel, and any directory it needs.
func writeRepoFile(t *testing.T, repoRoot, rel, content string) {
	t.Helper()
	path := filepath.Join(repoRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gitAddAll stages everything git will let it and returns the paths that landed
// in the index — what an operator's `git add -A` would commit.
func gitAddAll(t *testing.T, repoRoot string) []string {
	t.Helper()
	add := exec.Command("git", "-C", repoRoot, "add", "-A")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add -A: %v: %s", err, out)
	}
	out, err := exec.Command("git", "-C", repoRoot, "diff", "--cached", "--name-only", "-z").Output()
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	var staged []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			staged = append(staged, f)
		}
	}
	return staged
}

func readGitignore(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
