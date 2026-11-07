//go:build !windows

package mission

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestWriteExportFilesLocalStagingRollbackAndEXDEV(t *testing.T) {
	for _, failure := range []string{"none", "second-rename", "EXDEV"} {
		t.Run(failure, func(t *testing.T) {
			s, _ := authorityStore(t, 2)
			dir := t.TempDir() // --dir can name a directory outside the repository.
			a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
			require.NoError(t, os.WriteFile(a, []byte("value: old-a\n"), 0o600))
			require.NoError(t, os.WriteFile(b, []byte("value: old-b\n"), 0o600))
			original := renameFile
			t.Cleanup(func() { renameFile = original })
			calls := 0
			renameFile = func(from, to string) error {
				rel, err := filepath.Rel(s.policy.TempDir(), from)
				require.NoError(t, err)
				require.True(t, filepath.IsLocal(rel), "export and rollback staging must remain local: %s", from)
				assert.True(t, strings.HasSuffix(from, ".export"+layout.TmpMarker), "use the layout export staging pattern, not a source-scanner temp exemption")
				calls++
				if failure == "EXDEV" {
					return syscall.EXDEV
				}
				if failure == "second-rename" && calls == 2 {
					return errors.New("injected rename failure")
				}
				return original(from, to)
			}
			err := s.WriteExportFiles(TrackedFile{Path: a, Data: []byte("value: new-a\n"), Perm: 0o644},
				TrackedFile{Path: b, Data: []byte("value: new-b\n"), Perm: 0o644})
			if failure == "none" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				if failure == "EXDEV" {
					require.ErrorIs(t, err, syscall.EXDEV)
				}
				for path, want := range map[string]string{a: "value: old-a\n", b: "value: old-b\n"} {
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, want, string(data))
				}
			}
			entries, err := os.ReadDir(s.policy.TempDir())
			require.NoError(t, err)
			assert.Empty(t, entries, "export staging must be removed after success or failure")
			entries, err = os.ReadDir(dir)
			require.NoError(t, err)
			for _, entry := range entries {
				assert.False(t, strings.Contains(entry.Name(), ".ethos-tmp"))
			}
		})
	}
}

func TestWriteExportFilesRefusesMalformedAndSymlink(t *testing.T) {
	s, _ := authorityStore(t, 2)
	dir := t.TempDir()
	good, bad := filepath.Join(dir, "good.yaml"), filepath.Join(dir, "bad.yaml")
	require.Error(t, s.WriteExportFiles(TrackedFile{Path: good, Data: []byte("v: good\n"), Perm: 0o600},
		TrackedFile{Path: bad, Data: []byte("v: [broken\n"), Perm: 0o600}))
	assert.NoFileExists(t, good)
	assert.NoFileExists(t, bad)
	require.NoError(t, os.WriteFile(good, []byte("v: preserved\n"), 0o600))
	require.NoError(t, os.Symlink(good, bad))
	require.ErrorContains(t, s.WriteExportFiles(TrackedFile{Path: bad, Data: []byte("v: new\n"), Perm: 0o600}), "not a regular file")
	data, err := os.ReadFile(good)
	require.NoError(t, err)
	assert.Equal(t, "v: preserved\n", string(data))
}
