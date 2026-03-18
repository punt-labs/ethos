package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupExtTest(t *testing.T) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	require.NoError(t, s.Save(&Identity{Name: "Test", Handle: "test", Kind: "human"}))
	return s
}

func TestExtSetAndGet(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "beadle", "gpg_key_id", "3AA5C34371567BD2"))

	m, err := s.ExtGet("test", "beadle", "gpg_key_id")
	require.NoError(t, err)
	assert.Equal(t, "3AA5C34371567BD2", m["gpg_key_id"])
}

func TestExtGetAll(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "beadle", "gpg_key_id", "ABC"))
	require.NoError(t, s.ExtSet("test", "beadle", "imap_server", "mail.example.com"))

	m, err := s.ExtGet("test", "beadle", "")
	require.NoError(t, err)
	assert.Len(t, m, 2)
	assert.Equal(t, "ABC", m["gpg_key_id"])
	assert.Equal(t, "mail.example.com", m["imap_server"])
}

func TestExtGetNotFound(t *testing.T) {
	s := setupExtTest(t)
	_, err := s.ExtGet("test", "beadle", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtGetKeyNotFound(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "beadle", "gpg_key_id", "ABC"))
	_, err := s.ExtGet("test", "beadle", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtDel(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "beadle", "a", "1"))
	require.NoError(t, s.ExtSet("test", "beadle", "b", "2"))

	// Delete single key.
	require.NoError(t, s.ExtDel("test", "beadle", "a"))
	m, err := s.ExtGet("test", "beadle", "")
	require.NoError(t, err)
	assert.Len(t, m, 1)
	assert.Equal(t, "2", m["b"])
}

func TestExtDelLastKey(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "beadle", "a", "1"))
	require.NoError(t, s.ExtDel("test", "beadle", "a"))

	// File should be deleted when last key removed.
	_, err := s.ExtGet("test", "beadle", "")
	require.Error(t, err)
}

func TestExtDelNamespace(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "beadle", "a", "1"))
	require.NoError(t, s.ExtDel("test", "beadle", ""))

	_, err := s.ExtGet("test", "beadle", "")
	require.Error(t, err)
}

func TestExtList(t *testing.T) {
	s := setupExtTest(t)

	// Empty initially.
	ns, err := s.ExtList("test")
	require.NoError(t, err)
	assert.Empty(t, ns)

	// Add two namespaces.
	require.NoError(t, s.ExtSet("test", "beadle", "a", "1"))
	require.NoError(t, s.ExtSet("test", "biff", "b", "2"))

	ns, err = s.ExtList("test")
	require.NoError(t, err)
	assert.Len(t, ns, 2)
	assert.Contains(t, ns, "beadle")
	assert.Contains(t, ns, "biff")
}

func TestExtMergedView(t *testing.T) {
	s := setupExtTest(t)
	require.NoError(t, s.ExtSet("test", "beadle", "gpg_key_id", "ABC"))
	require.NoError(t, s.ExtSet("test", "biff", "tty", "s001"))

	id, err := s.Load("test")
	require.NoError(t, err)
	require.NotNil(t, id.Ext)
	assert.Equal(t, "ABC", id.Ext["beadle"]["gpg_key_id"])
	assert.Equal(t, "s001", id.Ext["biff"]["tty"])
}

func TestExtValidation_InvalidNamespace(t *testing.T) {
	s := setupExtTest(t)
	assert.Error(t, s.ExtSet("test", "INVALID", "key", "val"))
	assert.Error(t, s.ExtSet("test", "-bad", "key", "val"))
	assert.Error(t, s.ExtSet("test", "", "key", "val"))
}

func TestExtValidation_InvalidKey(t *testing.T) {
	s := setupExtTest(t)
	assert.Error(t, s.ExtSet("test", "beadle", "INVALID", "val"))
	assert.Error(t, s.ExtSet("test", "beadle", "-bad", "val"))
	assert.Error(t, s.ExtSet("test", "beadle", "", "val"))
}

func TestExtValidation_ValueTooLong(t *testing.T) {
	s := setupExtTest(t)
	long := make([]byte, MaxValueLen+1)
	for i := range long {
		long[i] = 'x'
	}
	assert.Error(t, s.ExtSet("test", "beadle", "key", string(long)))
}

func TestExtValidation_PersonaNotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	assert.Error(t, s.ExtSet("nonexistent", "beadle", "key", "val"))
}

func TestExtDirCreatedOnSave(t *testing.T) {
	s := NewStore(t.TempDir())
	require.NoError(t, s.Save(&Identity{Name: "Test", Handle: "test", Kind: "human"}))

	info, err := os.Stat(s.ExtDir("test"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestExtPathTraversal(t *testing.T) {
	s := NewStore("/root/ethos")
	assert.Equal(t, "/root/ethos/identities/test.ext", s.ExtDir("test"))
	assert.Equal(t, "/root/ethos/identities/passwd.ext", s.ExtDir("../../etc/passwd"))
	assert.Equal(t, filepath.Join("/root/ethos/identities/test.ext", "beadle.yaml"),
		s.extPath("test", "beadle"))
}
