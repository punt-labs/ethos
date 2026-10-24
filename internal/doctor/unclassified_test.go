package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func touch(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, nil, 0o644))
}

func TestCheckUnclassifiedFiles_NoRoot(t *testing.T) {
	r := CheckUnclassifiedFiles("")
	assert.Equal(t, "PASS", r.Status)
	assert.Equal(t, "not in a repo", r.Detail)
}

func TestCheckUnclassifiedFiles_NoEthosTrees(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "README.md") // outside the two zones, never examined
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "PASS", r.Status)
}

func TestCheckUnclassifiedFiles_ClassifiedFilesPass(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		".punt-labs/ethos/identities/bwk.yaml",
		".punt-labs/ethos/missions/m-1/contract.yaml",
		".punt-labs/ethos/missions/m-1/.lock",
		".punt-labs/ethos/missions/m-1/artifacts/notes.md",
		".punt-labs/local/ethos/sessions/s-1.audit.jsonl",
		".punt-labs/local/ethos/trace/missions/m-1/reflections.yaml",
	} {
		touch(t, root, rel)
	}
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "PASS", r.Status, r.Detail)
}

func TestCheckUnclassifiedFiles_WarnsAndListsUnmatched(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".punt-labs/ethos/identities/bwk.yaml")
	touch(t, root, ".punt-labs/ethos/ethos.yaml")
	touch(t, root, ".punt-labs/local/ethos/notes.txt")
	touch(t, root, ".punt-labs/vox/vox.md") // another tool's file: not ours to classify

	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, ".punt-labs/ethos/ethos.yaml")
	assert.Contains(t, r.Detail, ".punt-labs/local/ethos/notes.txt")
	assert.NotContains(t, r.Detail, "bwk.yaml")
	assert.NotContains(t, r.Detail, "vox.md")
	assert.True(t, r.Passed(), "WARN must not fail the command")
}

func TestCheckUnclassifiedFiles_ListsEachFileOnce(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".punt-labs/ethos/stray.bin")
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, 1, countOccurrences(r.Detail, ".punt-labs/ethos/stray.bin"), r.Detail)
}

func TestCheckUnclassifiedFiles_TruncatesLongList(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxListedUnclassified+5; i++ {
		touch(t, root, fmt.Sprintf(".punt-labs/ethos/stray/f%02d.bin", i))
	}
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, fmt.Sprintf("%d files", maxListedUnclassified+5))
	assert.Contains(t, r.Detail, "and 5 more")
	assert.NotContains(t, r.Detail, fmt.Sprintf("f%02d.bin", maxListedUnclassified+4))
}

func countOccurrences(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}

// TestCheckUnclassifiedFiles_HooksInsideZoneAreUnclassified: layout refuses
// hook entries for a hooks directory inside .punt-labs/, so with core.hooksPath
// pointing into a scanned zone its files warn like any other unknown file.
func TestCheckUnclassifiedFiles_HooksInsideZoneAreUnclassified(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "core.hooksPath", ".punt-labs/ethos/hooks"},
	} {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	touch(t, root, ".punt-labs/ethos/hooks/pre-commit")
	touch(t, root, ".punt-labs/ethos/hooks/commit-msg")
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, ".punt-labs/ethos/hooks/pre-commit")
	assert.Contains(t, r.Detail, ".punt-labs/ethos/hooks/commit-msg")
}

// TestCheckUnclassifiedFiles_ExportedMissionsPass: `ethos mission export --dir
// .punt-labs/ethos/missions-closed` output must not warn; other files beside it
// still do.
func TestCheckUnclassifiedFiles_ExportedMissionsPass(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".punt-labs/ethos/missions-closed/m-2026-10-24-001.contract.yaml")
	touch(t, root, ".punt-labs/ethos/missions-closed/m-2026-10-24-001.result.yaml")
	assert.Equal(t, "PASS", CheckUnclassifiedFiles(root).Status)

	touch(t, root, ".punt-labs/ethos/README.md")
	touch(t, root, ".punt-labs/ethos/missions-closed/notes.yaml")
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, ".punt-labs/ethos/README.md")
	assert.Contains(t, r.Detail, "missions-closed/notes.yaml")
}
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir,
		"-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// TestCheckUnclassifiedFiles_ScansTheWorktreeNotTheMainCheckout: in a linked
// worktree the check describes the worktree's files; the main checkout's
// stray file is not its business.
func TestCheckUnclassifiedFiles_ScansTheWorktreeNotTheMainCheckout(t *testing.T) {
	main := t.TempDir()
	gitIn(t, main, "init", "-q")
	touch(t, main, "README.md")
	gitIn(t, main, "add", "README.md")
	gitIn(t, main, "commit", "-q", "-m", "init")
	touch(t, main, ".punt-labs/ethos/ethos.yaml") // untracked: absent from the worktree
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, main, "worktree", "add", "-q", wt)

	assert.Equal(t, "WARN", CheckUnclassifiedFiles(main).Status)
	r := CheckUnclassifiedFiles(wt)
	assert.Equal(t, "PASS", r.Status, r.Detail)

	touch(t, wt, ".punt-labs/ethos/stray.bin")
	r = CheckUnclassifiedFiles(wt)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, ".punt-labs/ethos/stray.bin")
	assert.NotContains(t, r.Detail, "ethos.yaml")
}

func TestCheckUnclassifiedFiles_Pluralises(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".punt-labs/ethos/a.bin")
	r := CheckUnclassifiedFiles(root)
	assert.Contains(t, r.Detail, "1 file matches")
	touch(t, root, ".punt-labs/ethos/b.bin")
	r = CheckUnclassifiedFiles(root)
	assert.Contains(t, r.Detail, "2 files match")
}

func TestCheckUnclassifiedFiles_SymlinkZoneIsReported(t *testing.T) {
	for _, target := range []string{"dangling", "."} {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
		if err := os.Symlink(target, filepath.Join(root, ".punt-labs", "ethos")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		r := CheckUnclassifiedFiles(root)
		assert.Equal(t, "WARN", r.Status, target)
		assert.Contains(t, r.Detail, ".punt-labs/ethos is a symlink; not scanned")
	}
}

func TestCheckUnclassifiedFiles_SymlinkInsideZoneIsReported(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".punt-labs/ethos/identities/bwk.yaml")
	other := t.TempDir()
	touch(t, other, "hidden.bin")
	if err := os.Symlink(other, filepath.Join(root, ".punt-labs", "ethos", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, ".punt-labs/ethos/linked (symlink, not scanned)")
	assert.NotContains(t, r.Detail, "bwk.yaml")
}

func TestCheckUnclassifiedFiles_HooksDirUnderPuntLabsOutsideZones(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "config", "core.hooksPath", ".punt-labs/githooks")
	assert.Equal(t, "PASS", CheckUnclassifiedFiles(root).Status, "no hook files yet")

	touch(t, root, ".punt-labs/githooks/pre-commit")
	touch(t, root, ".punt-labs/githooks/commit-msg")
	touch(t, root, ".punt-labs/githooks/other") // not an ethos-written hook
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, ".punt-labs/githooks/pre-commit")
	assert.Contains(t, r.Detail, ".punt-labs/githooks/commit-msg")
	assert.NotContains(t, r.Detail, "githooks/other")
}

// TestCheckUnclassifiedFiles_SurfacesHooksDirWarnings: githook.HooksDir warns
// about a hooks directory inside the tree; the check shows it.
func TestCheckUnclassifiedFiles_SurfacesHooksDirWarnings(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "config", "core.hooksPath", ".githooks")
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "PASS", r.Status, r.Detail)
	assert.Contains(t, r.Detail, "hooks directory:")
}

func TestCheckUnclassifiedFiles_UnresolvableHooksDirFails(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitIn(t, root, "config", "core.hooksPath", "loop/hooks")
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "FAIL", r.Status)
	assert.Contains(t, r.Detail, "loop")
}
