package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

const legacyVoiceIdentity = "name: Voice Person\nhandle: vperson\nkind: human\nemail: voice@example.com\nvoice:\n  provider: say\n  voice: Samantha\n"

func writeLegacyVoiceStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	s := NewStore(root)
	require.NoError(t, os.MkdirAll(s.IdentitiesDir(), 0o700))
	require.NoError(t, os.WriteFile(s.Path("vperson"), []byte(legacyVoiceIdentity), 0o600))
	return s
}

func voxBytes(t *testing.T, s *Store) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.ExtDir("vperson"), "vox.yaml"))
	require.NoError(t, err)
	return string(b)
}

// Load and the repair step must apply one rule, whichever runs first.
func TestVoiceMigrationLoadAndStepAgree(t *testing.T) {
	viaLoad := writeLegacyVoiceStore(t)
	_, err := viaLoad.Load("vperson", Reference(true))
	require.NoError(t, err)
	_, _, _, err = viaLoad.MigrateLegacyVoices(viaLoad)
	require.NoError(t, err)

	viaStep := writeLegacyVoiceStore(t)
	_, _, _, err = viaStep.MigrateLegacyVoices(viaStep)
	require.NoError(t, err)
	_, err = viaStep.Load("vperson", Reference(true))
	require.NoError(t, err)

	assert.Equal(t, voxBytes(t, viaStep), voxBytes(t, viaLoad))
	assert.Contains(t, voxBytes(t, viaLoad), "voice: Samantha")
	assert.Contains(t, voxBytes(t, viaLoad), "provider: say")
}

func TestVoiceMigrationKeepsExistingExtensionValue(t *testing.T) {
	s := writeLegacyVoiceStore(t)
	require.NoError(t, os.MkdirAll(s.ExtDir("vperson"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(s.ExtDir("vperson"), "vox.yaml"), []byte("voice: Alex\n"), 0o600))
	_, err := s.Load("vperson", Reference(true))
	require.NoError(t, err)
	got, err := s.ExtGet("vperson", "vox", "")
	require.NoError(t, err)
	assert.Equal(t, "Alex", got["voice"], "a value set later is never overwritten")
	assert.Equal(t, "say", got["provider"])
}

func TestVoiceMigrationPreservesEveryOtherByte(t *testing.T) {
	const in = "# my comment\nname: Voice Person\nhandle: vperson # inline\nkind: human\nemail: voice@example.com\nvoice:\n  provider: say\n  voice: Samantha\n"
	const want = "# my comment\nname: Voice Person\nhandle: vperson # inline\nkind: human\nemail: voice@example.com\n"
	s := writeLegacyVoiceStore(t)
	require.NoError(t, os.WriteFile(s.Path("vperson"), []byte(in), 0o600))
	_, _, _, err := s.MigrateLegacyVoices(s)
	require.NoError(t, err)
	got, err := os.ReadFile(s.Path("vperson"))
	require.NoError(t, err)
	assert.Equal(t, want, string(got))
}

func TestMigrateLegacyVoicesSymlinkedIdentityIsResidual(t *testing.T) {
	s, _ := repositoryIdentityStore(t, layout.RecordBlame)
	link := filepath.Join(s.IdentitiesDir(), "alink.yaml")
	require.NoError(t, os.Symlink(s.Path("test"), link))

	moves, residuals, busy, err := s.MigrateLegacyVoices(s)
	require.NoError(t, err, "a refused symlink must not fail the step")
	assert.False(t, busy)
	assert.Empty(t, moves)
	require.Len(t, residuals, 1)
	assert.Equal(t, "alink", residuals[0].Handle)
	assert.Equal(t, link, residuals[0].Path)
}

func TestWithoutVoiceKeyRemovesInnerTrailingComment(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{
			"inner comment before next key",
			"name: Voice Person\nhandle: vperson\nvoice:\n  provider: say\n  # trailing inner\nkind: human\n",
			"name: Voice Person\nhandle: vperson\nkind: human\n",
		},
		{
			"head comment of next key stays",
			"voice:\n  provider: say\n  # inner\n# about kind\nkind: human\n",
			"# about kind\nkind: human\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := withoutVoiceKey([]byte(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}
