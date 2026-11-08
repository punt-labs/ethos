package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestConfigDurabilityThroughSetConfigKey(t *testing.T) {
	for _, failure := range []string{"file-sync", "directory-sync", "none"} {
		t.Run(failure, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			path := filepath.Join(root, ".punt-labs", "ethos.yaml")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			old := []byte("# preserve comment\nagent: old\nrecord: blame\n")
			want := []byte("# preserve comment\nagent: new\nrecord: blame\n")
			require.NoError(t, os.WriteFile(path, old, 0o644))
			fileSync, dirSync := syncConfigFile, syncConfigDestination
			t.Cleanup(func() { syncConfigFile, syncConfigDestination = fileSync, dirSync })
			injected := errors.New("injected config sync failure")
			var staged *os.File
			directoryCalled := false
			syncConfigFile = func(f *os.File) error {
				staged = f
				assert.Contains(t, f.Name(), filepath.Join(root, ".punt-labs", "local", "ethos", "tmp"))
				got, err := os.ReadFile(f.Name())
				require.NoError(t, err)
				assert.Equal(t, want, got)
				got, err = os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, old, got, "payload sync must precede installation")
				if failure == "file-sync" {
					return injected
				}
				return fileSync(f)
			}
			syncConfigDestination = func(p layout.Policy, dest string) error {
				directoryCalled = true
				require.NotNil(t, staged)
				_, err := staged.Stat()
				assert.ErrorIs(t, err, os.ErrClosed)
				assert.Equal(t, path, dest)
				got, err := os.ReadFile(dest)
				require.NoError(t, err)
				assert.Equal(t, want, got, "directory sync must follow installation")
				if failure == "directory-sync" {
					return injected
				}
				return dirSync(p, dest)
			}
			err = setConfigKey(root, "agent", "new")
			var applied *layout.InstallDurabilityError
			if failure == "none" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, injected)
				assert.Equal(t, failure == "directory-sync", errors.As(err, &applied))
			}
			got, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			if failure == "file-sync" {
				assert.Equal(t, old, got)
				assert.False(t, directoryCalled)
			} else {
				assert.Equal(t, want, got)
				assert.True(t, directoryCalled)
			}
			require.NotNil(t, staged)
			_, statErr := os.Stat(staged.Name())
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}
