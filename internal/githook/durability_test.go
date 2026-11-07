package githook

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

func TestRepositoryHookDurabilityThroughChainAndUnchain(t *testing.T) {
	for _, operation := range []string{"install", "chain", "refresh", "reduce"} {
		for _, failure := range []string{"file-sync", "directory-sync", "none"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				store, checkout := physicalTempDir(t), physicalTempDir(t)
				p, err := layout.NewRepository(store, checkout, layout.RecordFull)
				require.NoError(t, err)
				path := filepath.Join(checkout, "pre-commit")
				host := "#!/bin/sh\necho host\n"
				old, want, action := host, host+string(ExpectedSection(tag, src)), "chained"
				switch operation {
				case "install":
					old, want, action = "", string(markerForm(tag, src)), "installed"
				case "refresh":
					old = host + string(ExpectedSection(tag, []byte("#!/bin/sh\n"+ident+"\necho old\n")))
					action = "refreshed"
				case "reduce":
					old, want, action = want, host, "reduced"
				}
				if operation != "install" {
					require.NoError(t, os.WriteFile(path, []byte(old), 0o700))
				}
				fileSync, dirSync := syncHookFile, syncHookDestination
				t.Cleanup(func() { syncHookFile, syncHookDestination = fileSync, dirSync })
				injected := errors.New("injected hook sync failure")
				var staged *os.File
				directoryCalled := false
				syncHookFile = func(f *os.File) error {
					staged = f
					assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
					assert.Equal(t, want, read(t, f.Name()))
					info, err := f.Stat()
					require.NoError(t, err)
					if runtime.GOOS != "windows" {
						mode := os.FileMode(0o700)
						if operation == "install" {
							mode = 0o755
						}
						assert.Equal(t, mode, info.Mode().Perm(), "chmod must precede payload sync")
					}
					if operation == "install" {
						assert.NoFileExists(t, path)
					} else {
						assert.Equal(t, old, read(t, path))
					}
					if failure == "file-sync" {
						return injected
					}
					return fileSync(f)
				}
				syncHookDestination = func(got layout.Policy, dest string) error {
					directoryCalled = true
					require.NotNil(t, staged)
					_, err := staged.Stat()
					assert.ErrorIs(t, err, os.ErrClosed)
					assert.Equal(t, p, got)
					assert.Equal(t, want, read(t, dest))
					if failure == "directory-sync" {
						return injected
					}
					return dirSync(got, dest)
				}
				var result Result
				if operation == "reduce" {
					result, err = Unchain(path, tag, ident, WithRepository(p))
				} else {
					result, err = Chain(path, src, tag, ident, WithRepository(p))
				}
				var applied *layout.InstallDurabilityError
				if failure == "none" {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, injected)
					assert.Equal(t, failure == "directory-sync", errors.As(err, &applied))
				}
				assert.Equal(t, failure != "file-sync", directoryCalled)
				if failure == "file-sync" {
					assert.Empty(t, result.Action)
					if operation == "install" {
						assert.NoFileExists(t, path)
					} else {
						assert.Equal(t, old, read(t, path))
					}
				} else {
					assert.Equal(t, action, result.Action)
					assert.Equal(t, want, read(t, path))
				}
				entries, err := os.ReadDir(p.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries)
			})
		}
	}
}

func TestRepositoryHookDurabilityRetry(t *testing.T) {
	store, checkout := physicalTempDir(t), physicalTempDir(t)
	p, err := layout.NewRepository(store, checkout, layout.RecordFull)
	require.NoError(t, err)
	path := filepath.Join(checkout, "pre-commit")
	host := "#!/bin/sh\necho host\n"
	require.NoError(t, os.WriteFile(path, append([]byte(host), ExpectedSection(tag, src)...), 0o700))
	fileSync, dirSync := syncHookFile, syncHookDestination
	t.Cleanup(func() { syncHookFile, syncHookDestination = fileSync, dirSync })
	injected := errors.New("persistent hook directory sync failure")
	fault := true
	fileCalls, dirCalls := 0, 0
	syncHookFile = func(f *os.File) error {
		fileCalls++
		return fileSync(f)
	}
	syncHookDestination = func(got layout.Policy, dest string) error {
		dirCalls++
		assert.Equal(t, p, got)
		assert.Equal(t, path, dest)
		assert.Equal(t, host, read(t, dest))
		if fault {
			return injected
		}
		return dirSync(got, dest)
	}
	r, err := Unchain(path, tag, ident, WithRepository(p))
	require.ErrorIs(t, err, injected)
	var applied *layout.InstallDurabilityError
	require.ErrorAs(t, err, &applied)
	assert.Equal(t, path, applied.Path)
	assert.Equal(t, "reduced", r.Action)
	installed, err := os.Stat(path)
	require.NoError(t, err)
	for _, persist := range []bool{true, false} {
		fault = persist
		beforeCalls := dirCalls
		r, err = Unchain(path, tag, ident, WithRepository(p))
		if persist {
			assert.ErrorIs(t, err, injected)
			assert.ErrorAs(t, err, &applied)
		} else {
			assert.NoError(t, err)
		}
		assert.Equal(t, "noop", r.Action, "confirmation retry must not claim a new write")
		assert.Equal(t, beforeCalls+1, dirCalls)
		assert.Equal(t, 1, fileCalls)
		assert.Equal(t, host, read(t, path))
		current, err := os.Stat(path)
		require.NoError(t, err)
		assert.True(t, os.SameFile(installed, current))
	}
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestGlobalHookDoesNotUseRepositorySync(t *testing.T) {
	fileSync, dirSync := syncHookFile, syncHookDestination
	t.Cleanup(func() { syncHookFile, syncHookDestination = fileSync, dirSync })
	syncHookFile = func(*os.File) error { t.Fatal("global payload sync changed"); return nil }
	syncHookDestination = func(layout.Policy, string) error { t.Fatal("global directory sync changed"); return nil }
	path := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\necho host\n"
	require.NoError(t, os.WriteFile(path, []byte(host), 0o700))
	_, err := Chain(path, src, tag, ident)
	require.NoError(t, err)
	_, err = Unchain(path, tag, ident)
	require.NoError(t, err)
	r, err := Unchain(path, tag, ident)
	require.NoError(t, err)
	assert.Equal(t, "noop", r.Action)
	assert.Equal(t, host, read(t, path))
}
