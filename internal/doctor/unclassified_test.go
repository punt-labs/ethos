package doctor

import (
	"fmt"
	"os"
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
	r := CheckUnclassifiedFiles("", "")
	assert.Equal(t, "PASS", r.Status)
	assert.Equal(t, "not in a repo", r.Detail)
}

func TestCheckUnclassifiedFiles_NoEthosTrees(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "README.md") // outside the two zones, never examined
	r := CheckUnclassifiedFiles(root, root)
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
	r := CheckUnclassifiedFiles(root, root)
	assert.Equal(t, "PASS", r.Status, r.Detail)
}

func TestCheckUnclassifiedFiles_WarnsAndListsUnmatched(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".punt-labs/ethos/identities/bwk.yaml")
	touch(t, root, ".punt-labs/ethos/ethos.yaml")
	touch(t, root, ".punt-labs/local/ethos/notes.txt")
	touch(t, root, ".punt-labs/vox/vox.md") // another tool's file: not ours to classify

	r := CheckUnclassifiedFiles(root, root)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, ".punt-labs/ethos/ethos.yaml")
	assert.Contains(t, r.Detail, ".punt-labs/local/ethos/notes.txt")
	assert.NotContains(t, r.Detail, "bwk.yaml")
	assert.NotContains(t, r.Detail, "vox.md")
	assert.True(t, r.Passed(), "WARN must not fail the command")
}

func TestCheckUnclassifiedFiles_ScansCheckoutAndStore(t *testing.T) {
	checkout, store := t.TempDir(), t.TempDir()
	touch(t, checkout, ".punt-labs/local/ethos/stray.bin")
	touch(t, store, ".punt-labs/ethos/missions/m-1/stray.bin")

	r := CheckUnclassifiedFiles(checkout, store)
	assert.Equal(t, "WARN", r.Status)
	assert.Contains(t, r.Detail, ".punt-labs/local/ethos/stray.bin")
	assert.Contains(t, r.Detail, ".punt-labs/ethos/missions/m-1/stray.bin")
}

func TestCheckUnclassifiedFiles_ListsEachFileOnce(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".punt-labs/ethos/stray.bin")
	r := CheckUnclassifiedFiles(root, root)
	assert.Equal(t, 1, countOccurrences(r.Detail, ".punt-labs/ethos/stray.bin"), r.Detail)
}

func TestCheckUnclassifiedFiles_TruncatesLongList(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxListedUnclassified+5; i++ {
		touch(t, root, fmt.Sprintf(".punt-labs/ethos/stray/f%02d.bin", i))
	}
	r := CheckUnclassifiedFiles(root, root)
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
