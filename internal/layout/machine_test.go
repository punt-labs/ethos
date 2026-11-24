//go:build linux || darwin

package layout_test

import (
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/require"
)

func TestMachineRootIgnoresWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".punt-labs", "ethos")

	for _, dir := range []string{t.TempDir(), t.TempDir()} {
		t.Chdir(dir)
		root, err := layout.MachineRoot()
		require.NoError(t, err)
		require.Equal(t, want, root)
	}
}

func TestMachineRootRejectsUnusableHome(t *testing.T) {
	for _, home := range []string{"", "relative-home"} {
		t.Setenv("HOME", home)
		root, err := layout.MachineRoot()
		require.Error(t, err)
		require.Empty(t, root)
	}
}
