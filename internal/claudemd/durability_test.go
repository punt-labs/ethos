package claudemd

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryHostDurabilityThroughRegisterAndDeregister(t *testing.T) {
	for _, remove := range []bool{false, true} {
		for _, failure := range []string{"file-sync", "directory-sync", "none"} {
			t.Run(fmtHostCase(remove, failure), func(t *testing.T) {
				store, checkout := physicalTempDir(t), physicalTempDir(t)
				p, err := layout.NewRepository(store, checkout, layout.RecordFull)
				require.NoError(t, err)
				path := filepath.Join(checkout, "CLAUDE.md")
				old, want := "# host\r\nkeep\r\n", "# host\r\nkeep\r\n"+canonical+"\r\n"
				if remove {
					old, want = want, old
				}
				require.NoError(t, os.WriteFile(path, []byte(old), 0o600))
				require.NoError(t, os.Chmod(path, 0o640))
				fileSync, dirSync := syncHostFile, syncHostDestination
				t.Cleanup(func() { syncHostFile, syncHostDestination = fileSync, dirSync })
				injected := errors.New("injected host sync failure")
				var staged *os.File
				directoryCalled := false
				syncHostFile = func(f *os.File) error {
					staged = f
					assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
					assert.Equal(t, old, read(t, path))
					assert.Equal(t, want, read(t, f.Name()))
					info, err := f.Stat()
					require.NoError(t, err)
					if runtime.GOOS != "windows" {
						assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "chmod must precede payload sync")
					}
					if failure == "file-sync" {
						return injected
					}
					return fileSync(f)
				}
				syncHostDestination = func(got layout.Policy, dest string) error {
					directoryCalled = true
					require.NotNil(t, staged)
					_, err := staged.Stat()
					assert.ErrorIs(t, err, os.ErrClosed)
					assert.Equal(t, p, got)
					assert.Equal(t, path, dest)
					assert.Equal(t, want, read(t, dest))
					if failure == "directory-sync" {
						return injected
					}
					return dirSync(got, dest)
				}
				operation := Register
				if remove {
					operation = Deregister
				}
				wrote, err := operation(path, canonical, WithRepository(p))
				var applied *layout.InstallDurabilityError
				if failure == "none" {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, injected)
					assert.Equal(t, failure == "directory-sync", errors.As(err, &applied))
				}
				assert.Equal(t, failure != "file-sync", wrote)
				assert.Equal(t, failure != "file-sync", directoryCalled)
				if failure == "file-sync" {
					assert.Equal(t, old, read(t, path))
				} else {
					assert.Equal(t, want, read(t, path))
				}
				entries, err := os.ReadDir(p.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries)
			})
		}
	}
}

func fmtHostCase(remove bool, failure string) string {
	if remove {
		return "deregister/" + failure
	}
	return "register/" + failure
}

func TestRepositoryHostDurabilityRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove bool
		host   string
	}{
		{name: "register", host: "# host\r\nkeep\r\n"},
		{name: "deregister", remove: true, host: "# host\r\nkeep\r\n"},
		{name: "deregister-empty", remove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, checkout := physicalTempDir(t), physicalTempDir(t)
			p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
			require.NoError(t, err)
			path := filepath.Join(checkout, "CLAUDE.md")
			old, want := tc.host, tc.host+canonical+"\r\n"
			operation := Register
			if tc.remove {
				old, want = want, old
				operation = Deregister
			}
			require.NoError(t, os.WriteFile(path, []byte(old), 0o600))
			fileSync, dirSync := syncHostFile, syncHostDestination
			t.Cleanup(func() { syncHostFile, syncHostDestination = fileSync, dirSync })
			injected := errors.New("persistent host directory sync failure")
			fault := true
			fileCalls, dirCalls := 0, 0
			syncHostFile = func(f *os.File) error {
				fileCalls++
				return fileSync(f)
			}
			syncHostDestination = func(got layout.Policy, dest string) error {
				dirCalls++
				assert.Equal(t, p, got)
				assert.Equal(t, path, dest)
				assert.Equal(t, want, read(t, dest))
				if fault {
					return injected
				}
				return dirSync(got, dest)
			}
			wrote, err := operation(path, canonical, WithRepository(p))
			require.ErrorIs(t, err, injected)
			var applied *layout.InstallDurabilityError
			require.ErrorAs(t, err, &applied)
			assert.True(t, wrote)
			assert.Equal(t, path, applied.Path)
			installed, err := os.Stat(path)
			require.NoError(t, err)
			for _, persist := range []bool{true, false} {
				fault = persist
				beforeCalls := dirCalls
				wrote, err = operation(path, canonical, WithRepository(p))
				if persist {
					assert.ErrorIs(t, err, injected)
					assert.ErrorAs(t, err, &applied)
				} else {
					assert.NoError(t, err)
				}
				assert.False(t, wrote, "confirmation retry must not claim a new write")
				assert.Equal(t, beforeCalls+1, dirCalls)
				assert.Equal(t, 1, fileCalls, "confirmation retry must not rewrite the payload")
				assert.Equal(t, want, read(t, path))
				current, err := os.Stat(path)
				require.NoError(t, err)
				assert.True(t, os.SameFile(installed, current))
			}
			entries, err := os.ReadDir(p.TempDir())
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestGlobalHostDoesNotUseRepositorySync(t *testing.T) {
	fileSync, dirSync := syncHostFile, syncHostDestination
	t.Cleanup(func() { syncHostFile, syncHostDestination = fileSync, dirSync })
	syncHostFile = func(*os.File) error { t.Fatal("global payload sync changed"); return nil }
	syncHostDestination = func(layout.Policy, string) error { t.Fatal("global directory sync changed"); return nil }
	path := fixture(t, "CLAUDE.md", "host\n")
	wrote, err := Register(path, canonical)
	require.NoError(t, err)
	assert.True(t, wrote)
	wrote, err = Register(path, canonical)
	require.NoError(t, err)
	assert.False(t, wrote)
	wrote, err = Deregister(path, canonical)
	require.NoError(t, err)
	assert.True(t, wrote)
	wrote, err = Deregister(path, canonical)
	require.NoError(t, err)
	assert.False(t, wrote)
	assert.Equal(t, "host\n", read(t, path))
}

func TestRepositoryHostDurabilityNoopGuards(t *testing.T) {
	store, checkout := physicalTempDir(t), physicalTempDir(t)
	p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
	require.NoError(t, err)
	dirSync := syncHostDestination
	t.Cleanup(func() { syncHostDestination = dirSync })
	syncHostDestination = func(layout.Policy, string) error {
		t.Fatal("missing or malformed host must not reach directory confirmation")
		return nil
	}
	missing := filepath.Join(checkout, "missing", "CLAUDE.md")
	wrote, err := Deregister(missing, canonical, WithRepository(p))
	require.NoError(t, err)
	assert.False(t, wrote)
	assert.NoDirExists(t, filepath.Dir(missing))
	path := filepath.Join(checkout, "CLAUDE.md")
	malformed := "```\n" + canonical + "\n"
	require.NoError(t, os.WriteFile(path, []byte(malformed), 0o600))
	for _, operation := range []func(string, string, ...Option) (bool, error){Register, Deregister} {
		wrote, err := operation(path, canonical, WithRepository(p))
		require.ErrorContains(t, err, "unterminated")
		assert.False(t, wrote)
		assert.Equal(t, malformed, read(t, path))
	}
}
