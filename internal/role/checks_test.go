package role

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckReferences(t *testing.T) {
	repo, global := t.TempDir(), t.TempDir()
	s := NewStore(global)
	require.Error(t, s.Save(&Role{Name: "invalid", Checks: "../outside"}))
	require.NoError(t, os.MkdirAll(s.Dir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(s.Dir(), "invalid.yaml"), []byte("name: invalid\nchecks: '../outside'\n"), 0o600))
	_, err := s.Load("invalid")
	require.Error(t, err)
	ls := NewLayeredStore(repo, global)
	require.Error(t, ls.Save(&Role{Name: "missing", Checks: "project"}))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "checks"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "checks/project.yaml"), []byte("name: project\nchecks: []\n"), 0o600))
	require.NoError(t, ls.Save(&Role{Name: "writer", Checks: "project"}))
	r, err := ls.Load("writer")
	require.NoError(t, err)
	require.Equal(t, "project", r.Checks)
}
