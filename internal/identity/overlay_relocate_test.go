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
		moves, busy, err = s.RelocateLegacyOverlays()
		return err
	}))
	assert.True(t, busy, "a held identity lock defers the handle")
	assert.Empty(t, moves)
	assert.FileExists(t, legacy, "a deferred overlay is untouched")

	moves, busy, err := s.RelocateLegacyOverlays()
	require.NoError(t, err)
	assert.False(t, busy)
	require.Len(t, moves, 1)
	assert.NoFileExists(t, legacy)
	assert.FileExists(t, s.extLocalPath("test", "vox"))
}

func TestRelocateLegacyOverlaysGlobalStoreIsNoOp(t *testing.T) {
	s := NewStore(t.TempDir())
	moves, busy, err := s.RelocateLegacyOverlays()
	require.NoError(t, err)
	assert.False(t, busy)
	assert.Empty(t, moves)
}
