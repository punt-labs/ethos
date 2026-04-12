package mission

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWalkWriteSet exercises WalkWriteSet with fixture directories
// created inside t.TempDir. Each case builds its own filesystem
// layout so tests are independent and self-contained.
func TestWalkWriteSet(t *testing.T) {
	// mkFile creates a file at path relative to root, creating parent
	// directories as needed. The content is a single byte so the file
	// is non-empty.
	mkFile := func(t *testing.T, root, rel string) {
		t.Helper()
		abs := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte("x"), 0o644))
	}

	tests := []struct {
		name     string
		layout   []string // relative paths to create as files
		writeSet []string
		want     []string
	}{
		{
			name:     "single file entry",
			layout:   []string{"internal/mission/store.go"},
			writeSet: []string{"internal/mission/store.go"},
			want:     []string{"internal/mission/store.go"},
		},
		{
			name:     "directory entry walks recursively",
			layout:   []string{"internal/mission/store.go", "internal/mission/log.go"},
			writeSet: []string{"internal/mission"},
			want:     []string{"internal/mission/log.go", "internal/mission/store.go"},
		},
		{
			name:     "deep directory walk",
			layout:   []string{"a/b/c/d.go", "a/b/e.go", "a/f.go"},
			writeSet: []string{"a"},
			want:     []string{"a/b/c/d.go", "a/b/e.go", "a/f.go"},
		},
		{
			name:     "missing entry silently skipped",
			layout:   []string{"real.go"},
			writeSet: []string{"real.go", "ghost.go"},
			want:     []string{"real.go"},
		},
		{
			name:     "all entries missing returns nil",
			layout:   []string{"unrelated.go"},
			writeSet: []string{"missing_dir", "missing_file.go"},
			want:     nil,
		},
		{
			name:     "mixed file and directory entries",
			layout:   []string{"cmd/main.go", "internal/foo/bar.go", "internal/foo/baz.go"},
			writeSet: []string{"cmd/main.go", "internal/foo"},
			want:     []string{"cmd/main.go", "internal/foo/bar.go", "internal/foo/baz.go"},
		},
		{
			name:     "duplicate entry deduplicated",
			layout:   []string{"a.go"},
			writeSet: []string{"a.go", "a.go"},
			want:     []string{"a.go"},
		},
		{
			name: "overlapping file and directory entries deduplicated",
			layout: []string{
				"internal/mission/store.go",
				"internal/mission/log.go",
			},
			writeSet: []string{"internal/mission/store.go", "internal/mission"},
			want:     []string{"internal/mission/log.go", "internal/mission/store.go"},
		},
		{
			name:     "empty write set returns nil",
			layout:   []string{"a.go"},
			writeSet: nil,
			want:     nil,
		},
		{
			name:     "empty directory yields no files",
			layout:   nil,
			writeSet: []string{"empty_dir"},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, rel := range tt.layout {
				mkFile(t, root, rel)
			}
			// For the "empty directory" case, create the dir with no files.
			if tt.name == "empty directory yields no files" {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "empty_dir"), 0o755))
			}

			got, err := WalkWriteSet(root, tt.writeSet)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestWalkWriteSet_EmptyRoot verifies that an empty repoRoot returns nil.
func TestWalkWriteSet_EmptyRoot(t *testing.T) {
	got, err := WalkWriteSet("", []string{"a.go"})
	require.NoError(t, err)
	assert.Nil(t, got)
}
