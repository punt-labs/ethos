//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRejectSymlink_Policy is the table-driven pin for the package's
// symlink policy. The cases cover every shape a path can take at a
// loader call site: a regular file (allowed), a symlink (refused), a
// missing path (allowed — the follow-on open surfaces the absence),
// and a directory (allowed; the policy is per-path, not per-tree).
//
// The error string is part of the contract — operators grep for
// "refusing to follow symlink" in mission logs and reviewer comments,
// so the case assertions pin the substring.
func TestRejectSymlink_Policy(t *testing.T) {
	dir := t.TempDir()

	regular := filepath.Join(dir, "regular")
	require.NoError(t, os.WriteFile(regular, []byte("ok"), 0o600))

	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(target, []byte("target"), 0o600))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(target, link))

	missing := filepath.Join(dir, "missing")

	subdir := filepath.Join(dir, "subdir")
	require.NoError(t, os.Mkdir(subdir, 0o700))

	tests := []struct {
		name    string
		path    string
		wantErr bool
		substr  string
	}{
		{"regular file is allowed", regular, false, ""},
		{"symlink is refused", link, true, "refusing to follow symlink"},
		{"missing path is allowed", missing, false, ""},
		{"directory is allowed", subdir, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rejectSymlink(tt.path)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.substr)
			assert.Contains(t, err.Error(), tt.path,
				"error must name the offending path so operators can locate it")
		})
	}
}

// TestRejectSymlink_DanglingLink covers the dangling-symlink case: a
// symlink whose target does not exist. Lstat reports the link itself,
// so the policy still fires — a dangling symlink is just as dangerous
// as a live one because a later writer could create the target out
// from under the open.
func TestRejectSymlink_DanglingLink(t *testing.T) {
	dir := t.TempDir()
	dangling := filepath.Join(dir, "dangling")
	require.NoError(t, os.Symlink(filepath.Join(dir, "no-such-target"), dangling))

	err := rejectSymlink(dangling)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to follow symlink")
}
