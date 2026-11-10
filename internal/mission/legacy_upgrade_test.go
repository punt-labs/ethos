package mission

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
