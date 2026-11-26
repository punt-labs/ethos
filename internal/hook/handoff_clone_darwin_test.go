package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDarwinGateInputCopiesAcrossVolumes(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	want := []byte("verified input")
	require.NoError(t, os.WriteFile(source, want, 0o751))
	src, err := os.Open(source)
	require.NoError(t, err)
	defer src.Close()
	path := filepath.Join(dir, "input")
	require.NoError(t, cloneDarwinGateInput(t.Context(), src, path, true, func(int, int, string, int) error {
		return unix.EXDEV
	}))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, got)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o751), info.Mode().Perm())
}
