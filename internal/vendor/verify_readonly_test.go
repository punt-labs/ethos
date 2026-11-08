package vendor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckDoesNotMigrateLegacyIdentity(t *testing.T) {
	root := t.TempDir()
	ids := filepath.Join(root, "identities")
	require.NoError(t, os.MkdirAll(ids, 0o700))
	path := filepath.Join(ids, "agent.yaml")
	original := []byte("name: Agent\nhandle: agent\nkind: agent\nvoice:\n  provider: old-provider\n  voice_id: old-voice\n")
	require.NoError(t, os.WriteFile(path, original, 0o600))
	report, err := Check(root)
	require.NoError(t, err)
	assert.Contains(t, report.Handles, "agent")
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, actual, "verification must not rewrite repository input")
	_, err = os.Stat(filepath.Join(ids, "agent.ext"))
	require.ErrorIs(t, err, os.ErrNotExist, "verification must not create migrated extensions")
}
