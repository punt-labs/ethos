package enable

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryDepositDurability(t *testing.T) {
	for _, rel := range []string{manifestRel, guideRel, setupRel} {
		for _, failure := range []string{"file-sync", "directory-sync", "none"} {
			t.Run(filepath.Base(rel)+"/"+failure, func(t *testing.T) {
				store, checkout := physicalTempDir(t), physicalTempDir(t)
				p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
				require.NoError(t, err)
				old := map[string][]byte{
					manifestRel: manifestBytes([]string{setupRel, guideRel, manifestRel}),
					guideRel:    []byte("old guide\n"), setupRel: []byte("old setup\n"),
				}
				want := map[string][]byte{
					manifestRel: manifestBytes([]string{guideRel, setupRel, manifestRel}),
					guideRel:    []byte("new guide\n"), setupRel: []byte("new setup\n"),
				}
				for r, data := range old {
					path := filepath.Join(checkout, r)
					require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
					require.NoError(t, os.WriteFile(path, data, 0o644))
				}
				fileSync, dirSync := syncVendoredFile, syncVendoredDestination
				t.Cleanup(func() { syncVendoredFile, syncVendoredDestination = fileSync, dirSync })
				injected := errors.New("injected vendored sync failure")
				var staged *os.File
				fileCalled, directoryCalled := false, false
				syncVendoredFile = func(f *os.File) error {
					data, err := os.ReadFile(f.Name())
					require.NoError(t, err)
					if string(data) == string(want[rel]) {
						fileCalled = true
						staged = f
						assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
						assert.Equal(t, string(old[rel]), readFile(t, filepath.Join(checkout, rel)))
						if failure == "file-sync" {
							return injected
						}
					}
					return fileSync(f)
				}
				syncVendoredDestination = func(got layout.Policy, dest string) error {
					if dest == filepath.Join(checkout, rel) {
						directoryCalled = true
						require.NotNil(t, staged)
						_, err := staged.Stat()
						assert.ErrorIs(t, err, os.ErrClosed)
						assert.Equal(t, string(want[rel]), readFile(t, dest))
						if failure == "directory-sync" {
							return injected
						}
					}
					return dirSync(got, dest)
				}
				_, err = deposit(checkout, want[guideRel], want[setupRel], p)
				var applied *layout.InstallDurabilityError
				if failure == "none" {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, injected)
					assert.Equal(t, failure == "directory-sync", errors.As(err, &applied))
				}
				assert.True(t, fileCalled)
				assert.Equal(t, failure != "file-sync", directoryCalled)
				if failure == "file-sync" {
					assert.Equal(t, string(old[rel]), readFile(t, filepath.Join(checkout, rel)))
				} else {
					assert.Equal(t, string(want[rel]), readFile(t, filepath.Join(checkout, rel)))
				}
				entries, err := os.ReadDir(p.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries)
				// A failed content/directory confirmation remains safely retryable
				// under the existing manifest-first collision contract.
				syncVendoredFile, syncVendoredDestination = fileSync, dirSync
				_, err = deposit(checkout, want[guideRel], want[setupRel], p)
				require.NoError(t, err)
			})
		}
	}
}
