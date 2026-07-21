package audit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRelPathspec_Normalizes is C1/C2/C5 at the unit level: an absolute path is
// made repo-relative and slash-separated; a relative path is only
// slash-normalized. Repo-relative pathspecs are what git matches against the
// work-tree prefix — an absolute path under a symlinked root (macOS /tmp,
// TMPDIR) fails that match.
func TestRelPathspec_Normalizes(t *testing.T) {
	repoRoot := filepath.Join("/repo", "root")
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"absolute under root", filepath.Join(repoRoot, "a", "b.jsonl"), "a/b.jsonl"},
		{"already relative", filepath.Join("a", "b.jsonl"), "a/b.jsonl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := relPathspec(repoRoot, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("relPathspec(%q, %q) = %q, want %q", repoRoot, tc.in, got, tc.want)
			}
		})
	}
}

// TestGitAdd_SymlinkedRootStagesChunk is the C1/C2/C5 integration case: a chunk
// under a symlinked repo root must stage cleanly. Before the fix, git received
// the absolute path as a pathspec and — under a symlinked root — matched
// nothing, so the seal either failed closed or silently skipped staging.
func TestGitAdd_SymlinkedRootStagesChunk(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	runGit(t, real, "init")
	runGit(t, real, "config", "user.email", "t@example.com")
	runGit(t, real, "config", "user.name", "tester")

	// A chunk written under the symlinked root, staged by its absolute path.
	sub := filepath.Join(link, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	chunk := filepath.Join(sub, "audit-"+TSToField(1)+"-"+TSToField(2)+".jsonl")
	if err := os.WriteFile(chunk, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := GitAdd(link, chunk); err != nil {
		t.Fatalf("GitAdd under symlinked root: %v", err)
	}

	// git ls-files must now report the chunk as tracked (staged).
	tracked := runGit(t, link, "ls-files")
	if !strings.Contains(tracked, "sub/audit-") {
		t.Fatalf("chunk not staged; git ls-files = %q", tracked)
	}
}

// runGit runs a git subcommand in dir and returns its stdout, failing the test
// on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
