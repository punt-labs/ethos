package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
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
	assert.True(t, strings.HasPrefix(r.Detail, "not scanned: not in a repository"), r.Detail)
}

func TestCheckUnclassifiedFiles_NoEthosTrees(t *testing.T) {
	root := repoTemp(t)
	touch(t, root, "README.md") // outside the two zones, never examined
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "PASS", r.Status)
	assert.True(t, strings.HasPrefix(r.Detail, "not scanned: neither .punt-labs/ethos nor .punt-labs/local/ethos exists"), r.Detail)
}

func TestCheckUnclassifiedFiles_ClassifiedFilesPass(t *testing.T) {
	root := repoTemp(t)
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
	assert.False(t, strings.HasPrefix(r.Detail, "not scanned"), "zones were walked: %s", r.Detail)
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

// TestCheckUnclassifiedFiles_ExportedMissionsPass: `ethos mission export --dir
// .punt-labs/ethos/missions-closed` output must not warn; other files beside it
// still do.
func TestCheckUnclassifiedFiles_ExportedMissionsPass(t *testing.T) {
	root := repoTemp(t)
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
		assert.Contains(t, r.Detail, "not scanned: .punt-labs/ethos is a symlink")
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
	assert.Contains(t, r.Detail, "not scanned: .punt-labs/ethos/linked is a symlink")
	assert.NotContains(t, r.Detail, "bwk.yaml")
}

// TestCheckUnclassifiedFiles_HooksDirUnderPuntLabs: layout refuses hook entries
// for a hooks directory at or under .punt-labs/, so the check names the
// directory itself. Inside a zone the directory must not be classified by the
// zone's own entries, such as a mission's artifacts/ (class 3); the note
// reports the class each file actually gets, or "unclassified".
func TestCheckUnclassifiedFiles_HooksDirUnderPuntLabs(t *testing.T) {
	for dir, got := range map[string]string{
		".punt-labs/githooks":                     "unclassified",
		".punt-labs/hooks":                        "unclassified",
		".punt-labs/ethos/hooks":                  "unclassified",
		".punt-labs/ethos/missions/m-1/artifacts": "class 3 via .punt-labs/ethos/missions/*/artifacts/**",
	} {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			gitIn(t, root, "init", "-q")
			gitIn(t, root, "config", "core.hooksPath", dir)
			assert.Equal(t, "PASS", CheckUnclassifiedFiles(root).Status, "no hook files yet")

			touch(t, root, dir+"/pre-commit")
			touch(t, root, dir+"/commit-msg")
			r := CheckUnclassifiedFiles(root)
			assert.Equal(t, "WARN", r.Status)
			assert.Contains(t, r.Detail, "core.hooksPath "+dir+" is inside .punt-labs; layout derives no hook entries for this directory; "+
				"a zone entry may still match the files: commit-msg ("+got+"), pre-commit ("+got+")")
		})
	}
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

// TestCheckUnclassifiedFiles_NamesADegradedHooksLookup: with git off PATH the
// hooks directory is found by hand and core.hooksPath is not read; the check
// says so rather than classifying against a directory it may have guessed.
func TestCheckUnclassifiedFiles_NamesADegradedHooksLookup(t *testing.T) {
	root := t.TempDir()
	testhelpers.MakeGitDir(t, root)
	touch(t, root, ".punt-labs/ethos/identities/bwk.yaml")
	t.Setenv("PATH", t.TempDir()) // no git

	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status, r.Detail)
	assert.Contains(t, r.Detail, "hooks directory not verified:")
	assert.Contains(t, r.Detail, "git")
}

// TestCheckUnclassifiedFiles_SubdirectoryOfAnotherRepoIsNotVerified: git
// reports the enclosing repository's work tree, so the hooks directory it would
// name is not this root's; a clean scan still cannot say PASS.
func TestCheckUnclassifiedFiles_SubdirectoryOfAnotherRepoIsNotVerified(t *testing.T) {
	parent := t.TempDir()
	gitIn(t, parent, "init", "-q")
	root := filepath.Join(parent, "sub")
	touch(t, root, ".punt-labs/ethos/identities/bwk.yaml")
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status, r.Detail)
	assert.Contains(t, r.Detail, "hooks directory not verified:")
	assert.Contains(t, r.Detail, "work tree")
}

// gitlink writes root/rel as a gitfile naming a real git directory, as a
// submodule's .git is.
func gitlink(t *testing.T, root, rel string) {
	t.Helper()
	target := t.TempDir()
	testhelpers.MakeGitDir(t, target)
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("gitdir: "+filepath.Join(target, ".git")+"\n"), 0o644))
}

// TestCheckUnclassifiedFiles_InvalidGitIsNotAWorkingTree: git does not treat an
// empty .git file, a dangling symlink or an empty .git directory as a
// repository, so the directory is scanned like any other.
func TestCheckUnclassifiedFiles_InvalidGitIsNotAWorkingTree(t *testing.T) {
	cases := map[string]func(t *testing.T, dot string){
		"empty file": func(t *testing.T, dot string) { require.NoError(t, os.WriteFile(dot, nil, 0o644)) },
		"dangling symlink": func(t *testing.T, dot string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(dot), "absent"), dot); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		},
		"empty directory": func(t *testing.T, dot string) { require.NoError(t, os.Mkdir(dot, 0o755)) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			gitIn(t, root, "init", "-q")
			touch(t, root, ".punt-labs/ethos/identities/bwk.yaml")
			touch(t, root, ".punt-labs/ethos/vendored/stray.bin")
			mk(t, filepath.Join(root, ".punt-labs", "ethos", "vendored", ".git"))
			r := CheckUnclassifiedFiles(root)
			assert.Equal(t, "WARN", r.Status, r.Detail)
			assert.Contains(t, r.Detail, "stray.bin")
			assert.NotContains(t, r.Detail, "separate git working tree")
		})
	}
}

// TestCheckUnclassifiedFiles_NestedWorkingTreeIsNotScanned: a directory with
// its own .git inside a zone (a submodule) is another repository's files.
func TestCheckUnclassifiedFiles_NestedWorkingTreeIsNotScanned(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	touch(t, root, ".punt-labs/ethos/identities/bwk.yaml")
	touch(t, root, ".punt-labs/ethos/vendored/stray.bin")
	gitlink(t, root, ".punt-labs/ethos/vendored/.git")
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status, r.Detail)
	assert.Contains(t, r.Detail, "not scanned: .punt-labs/ethos/vendored is a separate git working tree")
	assert.NotContains(t, r.Detail, "stray.bin")
}

// TestCheckUnclassifiedFiles_ZoneRootSubmoduleIsNotScanned: the zone root
// itself being a submodule is skipped whole.
func TestCheckUnclassifiedFiles_ZoneRootSubmoduleIsNotScanned(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	touch(t, root, ".punt-labs/ethos/stray.bin")
	gitlink(t, root, ".punt-labs/ethos/.git")
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status, r.Detail)
	assert.Contains(t, r.Detail, "not scanned: .punt-labs/ethos is a separate git working tree")
	assert.NotContains(t, r.Detail, "stray.bin")
}

// TestWalkZone_MissingRootIsNotScanned: a zone removed between the Lstat and
// the walk was not scanned, and says so.
func TestWalkZone_MissingRootIsNotScanned(t *testing.T) {
	sc := newScan()
	scanned, err := sc.walkZone(nil, t.TempDir(), filepath.Join(".punt-labs", "ethos"))
	require.NoError(t, err)
	assert.False(t, scanned)
}

// TestCheckUnclassifiedFiles_RelativeRoot: ETHOS_REPO_ROOT may be relative.
func TestCheckUnclassifiedFiles_RelativeRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	gitIn(t, parent, "init", "-q", "repo")
	gitIn(t, root, "config", "core.hooksPath", ".githooks")
	touch(t, root, ".punt-labs/ethos/stray.bin")
	t.Chdir(parent)

	r := CheckUnclassifiedFiles("repo")
	assert.Equal(t, "WARN", r.Status, r.Detail)
	assert.Contains(t, r.Detail, ".punt-labs/ethos/stray.bin")
	assert.NotContains(t, r.Detail, "git did not resolve", "git answered for the absolute root")
}

// TestCheckUnclassifiedFiles_SymlinkedParentIsReported: a symlinked .punt-labs
// (or .punt-labs/local) is followed by neither zone; the first symlink on the
// way down is the one reported.
func TestCheckUnclassifiedFiles_SymlinkedParentIsReported(t *testing.T) {
	for _, tt := range []struct{ link, want string }{
		{".punt-labs", ".punt-labs is a symlink"},
		{".punt-labs/local", ".punt-labs/local is a symlink"},
	} {
		root := t.TempDir()
		outside := t.TempDir()
		touch(t, outside, "ethos/stray.bin")
		touch(t, outside, "local/ethos/stray.bin")
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
		if tt.link == ".punt-labs" {
			require.NoError(t, os.Remove(filepath.Join(root, ".punt-labs")))
		}
		if err := os.Symlink(outside, filepath.Join(root, filepath.FromSlash(tt.link))); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		r := CheckUnclassifiedFiles(root)
		assert.Equal(t, "WARN", r.Status, tt.link)
		assert.Contains(t, r.Detail, "not scanned: "+tt.want, tt.link)
		assert.NotContains(t, r.Detail, "stray.bin", "a file behind the link was read: %s", tt.link)
		assert.NotContains(t, r.Detail, "match", "no file was scanned, so none is counted: %s", tt.link)
	}
}

// TestCheckUnclassifiedFiles_CountsOnlyFiles: files, what was not scanned, and
// the hooks-directory line are reported separately; only files are counted.
func TestCheckUnclassifiedFiles_CountsOnlyFiles(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "config", "core.hooksPath", ".punt-labs/githooks")
	touch(t, root, ".punt-labs/githooks/pre-commit")
	touch(t, root, ".punt-labs/ethos/stray.bin")
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(root, ".punt-labs", "ethos", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, "1 file matches no entry of the path-class table: .punt-labs/ethos/stray.bin")
	assert.Contains(t, r.Detail, "not scanned: .punt-labs/ethos/linked is a symlink")
	assert.Contains(t, r.Detail, "core.hooksPath .punt-labs/githooks is inside .punt-labs")
}

// TestCheckUnclassifiedFiles_OnlySkippedItemsAreNotFiles: nothing unclassified,
// one thing not scanned: no "files match" count at all.
func TestCheckUnclassifiedFiles_OnlySkippedItemsAreNotFiles(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".punt-labs/ethos/identities/bwk.yaml")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".punt-labs", "ethos", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status)
	assert.NotContains(t, r.Detail, "match")
	assert.Contains(t, r.Detail, "not scanned: .punt-labs/ethos/linked is a symlink")
}

// repoTemp returns a fresh git repository. A plain temp directory under a
// TMPDIR that is itself inside a repository is a subdirectory of that repo,
// which the hooks lookup reports as degraded.
func repoTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	return dir
}

// TestCheckUnclassifiedFiles_DegradedKeepsTheScanResult: an unverified hooks
// directory adds to the finding; it does not replace it.
func TestCheckUnclassifiedFiles_DegradedKeepsTheScanResult(t *testing.T) {
	root := t.TempDir()
	testhelpers.MakeGitDir(t, root)
	touch(t, root, ".punt-labs/ethos/stray.bin")
	t.Setenv("PATH", t.TempDir()) // no git

	r := CheckUnclassifiedFiles(root)
	assert.Equal(t, "WARN", r.Status, r.Detail)
	assert.Contains(t, r.Detail, ".punt-labs/ethos/stray.bin")
	assert.Contains(t, r.Detail, "hooks directory not verified:")

	clean := t.TempDir()
	testhelpers.MakeGitDir(t, clean)
	touch(t, clean, ".punt-labs/ethos/identities/bwk.yaml")
	r = CheckUnclassifiedFiles(clean)
	assert.Equal(t, "WARN", r.Status, r.Detail)
	assert.Contains(t, r.Detail, "every file under .punt-labs/ethos")
	assert.Contains(t, r.Detail, "hooks directory not verified:")
}

// TestDropSameFile: paths are matched by file identity, not spelling; a hard
// link stands in for the second spelling a case-insensitive file system gives.
func TestDropSameFile(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "a/hook")
	touch(t, root, "a/other")
	require.NoError(t, os.Link(filepath.Join(root, "a", "hook"), filepath.Join(root, "a", "Hook")))
	info, err := os.Lstat(filepath.Join(root, "a", "hook"))
	require.NoError(t, err)

	files := map[string]bool{"a/Hook": true, "a/other": true, "a/vanished": true}
	require.NoError(t, dropSameFile(files, root, info))
	assert.Equal(t, map[string]bool{"a/other": true, "a/vanished": true}, files)
}
