package audit

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestPublicationActualIndexStates(t *testing.T) {
	for _, selection := range []string{"default", "alternate-absolute", "alternate-relative"} {
		for _, state := range []string{"empty", "malformed", "valid", "absent"} {
			t.Run(selection+"/"+state, func(t *testing.T) {
				p, path, ordinary := publicationFixture(t)
				root := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
				// All Git writes belong to this disposable fixture. HEAD and an
				// unrelated staged change must not become trace-only publication.
				runGit(t, root, "-c", "core.hooksPath=", "-c", "commit.gpgsign=false", "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "source baseline")
				require.NoError(t, os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("staged unrelated\n"), 0o600))
				runGit(t, root, "add", "--", "unrelated.txt")
				require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("unstaged source\n"), 0o600))
				valid, err := os.ReadFile(ordinary)
				require.NoError(t, err)
				index := ordinary
				if selection != "default" {
					index = filepath.Join(t.TempDir(), "alternate-index")
					require.NoError(t, os.WriteFile(index, valid, 0o600))
					selected := index
					if selection == "alternate-relative" {
						selected, err = filepath.Rel(root, index)
						require.NoError(t, err)
					}
					t.Setenv("GIT_INDEX_FILE", selected)
				}
				switch state {
				case "empty":
					require.NoError(t, os.WriteFile(index, nil, 0o600))
				case "malformed":
					require.NoError(t, os.WriteFile(index, []byte("invalid Git index\n"), 0o600))
				case "absent":
					require.NoError(t, os.Remove(index))
				}
				var before []byte
				var original fs.FileInfo
				if state != "absent" {
					require.NoError(t, os.Chmod(index, 0o640))
					before, err = os.ReadFile(index)
					require.NoError(t, err)
					original, err = os.Stat(index)
					require.NoError(t, err)
				}
				err = StagePublication(p, []string{path})
				t.Logf("StagePublication: err=%v; before bytes=%d sha256=%x", err, len(before), sha256.Sum256(before))
				if state == "empty" || state == "malformed" {
					assert.Error(t, err, "corrupt actual index must not be initialized")
					if err != nil && state == "empty" {
						assert.Contains(t, err.Error(), index)
						assert.Contains(t, err.Error(), "empty/corrupt")
						assert.Contains(t, err.Error(), "actual index unchanged")
					}
					if err != nil && state == "malformed" {
						assert.Contains(t, err.Error(), "staging audit publication (actual index unchanged)")
					}
					after, readErr := os.ReadFile(index)
					require.NoError(t, readErr)
					assert.Equal(t, before, after, "failed publication changed the actual index")
					info, statErr := os.Stat(index)
					require.NoError(t, statErr)
					assert.Equal(t, original.Mode(), info.Mode(), "failed publication changed the index mode")
					assert.True(t, os.SameFile(original, info), "failed publication replaced the actual index")
					t.Logf("after bytes=%d sha256=%x", len(after), sha256.Sum256(after))
				} else {
					require.NoError(t, err)
					for file, want := range map[string]string{
						"source.txt": "staged source\n", "unrelated.txt": "staged unrelated\n",
					} {
						data, present, err := IndexBlob(root, filepath.Join(root, file))
						require.NoError(t, err)
						assert.Equal(t, state == "valid", present)
						if state == "valid" {
							assert.Equal(t, want, string(data), "preserve staged, not working-tree, content")
						}
					}
					_, present, err := IndexBlob(root, path)
					require.NoError(t, err)
					assert.True(t, present, "successful initialization/publication must stage the chunk")
				}
				if selection != "default" {
					data, err := os.ReadFile(ordinary)
					require.NoError(t, err)
					assert.Equal(t, valid, data, "alternate selection changed the default index")
				}
				assertPublicationIndexClean(t, p, index, path)
			})
		}
	}
}

func TestPublicationEmptySymlinkIndexPreserved(t *testing.T) {
	p, path, index := publicationFixture(t)
	target := filepath.Join(t.TempDir(), "empty-index-target")
	require.NoError(t, os.WriteFile(target, nil, 0o640))
	original, err := os.Stat(target)
	require.NoError(t, err)
	require.NoError(t, os.Remove(index))
	if err := os.Symlink(target, index); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	err = StagePublication(p, []string{path})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty/corrupt")
	assert.Contains(t, err.Error(), index)
	link, err := os.Readlink(index)
	require.NoError(t, err, "do not replace the existing index symlink")
	assert.Equal(t, target, link)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Empty(t, data)
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, original.Mode(), info.Mode())
	assert.True(t, os.SameFile(original, info))
	assertPublicationIndexClean(t, p, index, path)
}

func assertPublicationIndexClean(t *testing.T, p layout.Policy, index, publication string) {
	t.Helper()
	_, err := os.Lstat(index + ".lock")
	assert.ErrorIs(t, err, os.ErrNotExist, "release the publication lock on every return")
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries, "no candidate index or candidate lock debris")
	var records []string
	require.NoError(t, filepath.WalkDir(p.CheckoutRecordRoot(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			records = append(records, path)
			assert.False(t, strings.HasSuffix(path, ".lock"), "no record-tree lock")
		}
		return nil
	}))
	assert.Equal(t, []string{publication}, records, "no leaked record-tree temporary")
}
