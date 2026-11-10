package mission

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// A crash after the corrections rename and before the contract rename leaves
// corrections without decisions. The next run completes the import and leaves
// the corrections bytes as the crash left them.
func TestUpgradeLegacyAuthority_ResumesAfterCrashBetweenRenames(t *testing.T) {
	s, c, root := legacyReflectionReader(t, true, 1)
	dir := filepath.Join(root, ".punt-labs", "ethos", "missions", c.MissionID)

	writes := 0
	s.persistenceCheck = func(phase string) error {
		if phase != "mission_write" {
			return nil
		}
		writes++
		if writes == 2 {
			panic("crash between the corrections rename and the contract rename")
		}
		return nil
	}
	require.Panics(t, func() { _, _ = s.UpgradeLegacyAuthority(c.MissionID) })
	s.persistenceCheck = nil

	corrections, err := os.ReadFile(filepath.Join(dir, "corrections.yaml"))
	require.NoError(t, err, "the crash happened after the corrections rename")
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	require.Nil(t, loaded.RoundDecisions, "the crash happened before the contract rename")

	upgraded, err := s.UpgradeLegacyAuthority(c.MissionID)
	require.NoError(t, err)
	assert.True(t, upgraded)
	loaded, err = s.Load(c.MissionID)
	require.NoError(t, err)
	require.NotNil(t, loaded.RoundDecisions)
	after, err := os.ReadFile(filepath.Join(dir, "corrections.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(corrections), string(after), "corrections kept as the crash left them")

	upgraded, err = s.UpgradeLegacyAuthority(c.MissionID)
	require.NoError(t, err)
	assert.False(t, upgraded, "second run is a no-op")
}

// A directory-sync failure after the rename leaves the new bytes installed, so
// the file is rewritten: ReplaceRecordFile reports the durability failure apart
// from the error.
func TestReplaceRecordFile_DurabilityFailureIsNotAnError(t *testing.T) {
	root := t.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	dest := filepath.Join(root, ".punt-labs", "ethos", "missions", "m-2026-11-04-001", "contract.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o700))
	require.NoError(t, os.WriteFile(dest, []byte("mission_id: m-2026-11-04-001\n"), 0o600))

	real := syncDir
	t.Cleanup(func() { syncDir = real })
	syncDir = func(string) error { return errors.New("injected sync failure") }

	durability, err := ReplaceRecordFile(p, dest, []byte("mission_id: m-2026-11-04-001\ncontext: x\n"))
	require.NoError(t, err, "the rename installed the bytes")
	assert.ErrorContains(t, durability, "injected sync failure")
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Contains(t, string(got), "context: x")
}
