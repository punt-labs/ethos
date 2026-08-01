package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/enable"
)

// A repo that already excludes machine-local files must come back from setup
// and vendor --apply untouched, whatever spelling it uses. String-matching one
// blessed rule re-appended a narrower near-duplicate on every run and dirtied
// the tree (Bugbot, PR #422).
func TestEnsureLocalExtIgnoredLeavesCoveredRepoAlone(t *testing.T) {
	cases := []struct {
		name      string
		gitignore string
	}{
		{"canonical rule", enable.LocalIgnoreRule + "\n"},
		{"legacy narrow rule", ".punt-labs/ethos/**/*.local.yaml\n"},
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

func readGitignore(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
