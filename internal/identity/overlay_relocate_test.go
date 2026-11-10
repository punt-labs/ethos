package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRelocateLegacyOverlaysBusyLockDefers(t *testing.T) {
	s, _ := repositoryIdentityStore(t, layout.RecordBlame)
	legacy := s.legacyExtLocalPath("test", "vox")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o700))
	require.NoError(t, os.WriteFile(legacy, []byte("voice: x\n"), 0o600))

	var moves []OverlayMove
	var busy bool
	require.NoError(t, s.withLock("test", func() error {
		var err error
		moves, _, busy, err = s.RelocateLegacyOverlays()
		return err
	}))
	assert.True(t, busy, "a held identity lock defers the handle")
	assert.Empty(t, moves)
	assert.FileExists(t, legacy, "a deferred overlay is untouched")

	moves, _, busy, err := s.RelocateLegacyOverlays()
	require.NoError(t, err)
	assert.False(t, busy)
	require.Len(t, moves, 1)
	assert.NoFileExists(t, legacy)
	assert.FileExists(t, s.extLocalPath("test", "vox"))
}

func TestRelocateLegacyOverlaysUnreadableLocalKeepsLegacy(t *testing.T) {
	s, _ := repositoryIdentityStore(t, layout.RecordBlame)
	legacy := s.legacyExtLocalPath("test", "vox")
	local := s.extLocalPath("test", "vox")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o700))
	require.NoError(t, os.WriteFile(legacy, []byte("voice: x\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o700))
	require.NoError(t, os.WriteFile(local, []byte("{{{ not yaml"), 0o600))

	moves, residuals, busy, err := s.RelocateLegacyOverlays()
	require.NoError(t, err)
	assert.False(t, busy)
	assert.Empty(t, moves)
	require.Len(t, residuals, 1)
	assert.Equal(t, "test/vox", residuals[0].ID)
	assert.Equal(t, "local overlay unreadable; legacy copy kept", residuals[0].Reason)
	assert.FileExists(t, legacy, "the only readable copy is never deleted")
	got, err := os.ReadFile(local)
	require.NoError(t, err)
	assert.Equal(t, "{{{ not yaml", string(got), "the unreadable new copy is left as it is")
}

func TestRelocateLegacyOverlaysReadableLocalDeletesLegacy(t *testing.T) {
	s, _ := repositoryIdentityStore(t, layout.RecordBlame)
	legacy := s.legacyExtLocalPath("test", "vox")
	local := s.extLocalPath("test", "vox")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o700))
	require.NoError(t, os.WriteFile(legacy, []byte("voice: old\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o700))
	require.NoError(t, os.WriteFile(local, []byte("voice: new\n"), 0o600))

	moves, residuals, _, err := s.RelocateLegacyOverlays()
	require.NoError(t, err)
	assert.Empty(t, residuals)
	require.Len(t, moves, 1)
	assert.True(t, moves[0].Kept)
	assert.NoFileExists(t, legacy)
	got, err := os.ReadFile(local)
	require.NoError(t, err)
	assert.Equal(t, "voice: new\n", string(got))
}

func TestRelocateLegacyOverlaysMovedFileIs0600(t *testing.T) {
	s, _ := repositoryIdentityStore(t, layout.RecordBlame)
	legacy := s.legacyExtLocalPath("test", "vox")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o700))
	require.NoError(t, os.WriteFile(legacy, []byte("voice: x\n"), 0o644))
	require.NoError(t, os.Chmod(legacy, 0o644))

	_, _, _, err := s.RelocateLegacyOverlays()
	require.NoError(t, err)
	info, err := os.Stat(s.extLocalPath("test", "vox"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestRelocateLegacyOverlaysSymlinkIsNotAnError(t *testing.T) {
	s, _ := repositoryIdentityStore(t, layout.RecordBlame)
	legacy := s.legacyExtLocalPath("test", "vox")
	target := filepath.Join(t.TempDir(), "elsewhere.yaml")
	require.NoError(t, os.WriteFile(target, []byte("voice: x\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o700))
	require.NoError(t, os.Symlink(target, legacy))

	moves, residuals, busy, err := s.RelocateLegacyOverlays()
	require.NoError(t, err, "a refused symlink must not fail the step")
	assert.False(t, busy)
	assert.Empty(t, moves)
	require.Len(t, residuals, 1)
	assert.Equal(t, "test/vox", residuals[0].ID)
	assert.Contains(t, residuals[0].Reason, legacy, "the residual names the path")
	_, err = os.Lstat(legacy)
	assert.NoError(t, err, "the symlink is left in place")
}

func TestRelocateLegacyOverlaysGlobalStoreIsNoOp(t *testing.T) {
	s := NewStore(t.TempDir())
	moves, residuals, busy, err := s.RelocateLegacyOverlays()
	require.NoError(t, err)
	assert.False(t, busy)
	assert.Empty(t, moves)
	assert.Empty(t, residuals)
}
