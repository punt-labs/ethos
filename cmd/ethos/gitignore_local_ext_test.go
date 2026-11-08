package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// A proven directory/ancestor exclusion must remain untouched on setup/vendor.
// String-matching one blessed rule dirtied covered repos (Bugbot, PR #422), but
// wildcard contents matches alone cannot establish directory exclusion. Preserve
// those user rules while conservatively adding one canonical rule, then leave
// the repaired file untouched on retry.
func TestEnsureLocalExtIgnoredLeavesCoveredRepoAlone(t *testing.T) {
	cases := []struct {
		name      string
		gitignore string
		wantAdded bool
	}{
		{"canonical rule", enable.LocalIgnoreRule + "\n", false},
		{"literal ancestor directory", ".punt-labs/\n", false},
		{"ambiguous whole subtree requires canonical repair", ".punt-labs/**\n", true},
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
			if added != c.wantAdded {
				t.Errorf("added = %v, want %v", added, c.wantAdded)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := c.gitignore
			if c.wantAdded {
				want += "\n" + enable.LocalIgnoreNote + "\n" + enable.LocalIgnoreRule + "\n"
				if n := strings.Count("\n"+string(got), "\n"+enable.LocalIgnoreRule+"\n"); n != 1 {
					t.Errorf("canonical rule appears %d times, want 1", n)
				}
			}
			if string(got) != want {
				t.Errorf(".gitignore bytes:\nwant:\n%s\ngot:\n%s", want, got)
			}
			added, err = ensureLocalExtIgnored(dir)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if added {
				t.Error("retry appended another rule")
			}
			if again := readGitignore(t, path); again != string(got) {
				t.Errorf("retry changed user policy or duplicated repair:\nwant:\n%s\ngot:\n%s", got, again)
			}
			writeRepoFile(t, dir, ".punt-labs/local/unforeseen", "synthetic private data\n")
			for _, staged := range gitAddAll(t, dir) {
				if strings.HasPrefix(staged, enable.LocalIgnoreRule) {
					t.Errorf("ordinary add staged private path %q", staged)
				}
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

// A legacy source-tree rule does not cover the shared local zone. Keep that
// protection while adding the canonical local rule; this helper does not move
// another product's credentials or retire its existing ignore protection.
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

	// The shared local zone protects every product and file extension.
	for _, f := range []string{".punt-labs/local/vox/vox.local.md", ".punt-labs/local/beadle/creds.local.json"} {
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
	add := testhelpers.GitCommand("-C", repoRoot, "add", "-A")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add -A: %v: %s", err, out)
	}
	out, err := testhelpers.GitCommand("-C", repoRoot, "diff", "--cached", "--name-only", "-z").Output()
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
