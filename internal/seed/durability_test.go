package seed

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositorySeedAgentDurabilityThroughSeedVersion(t *testing.T) {
	for _, operation := range []string{"create", "replace", "repair"} {
		for _, failure := range []string{"file-sync", "directory-sync", "none"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				store, checkout := physicalTempDir(t), physicalTempDir(t)
				p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
				require.NoError(t, err)
				global := t.TempDir()
				dest, skills := filepath.Join(global, "ethos"), filepath.Join(global, "skills")
				agents := filepath.Join(checkout, ".claude", "agents")
				path := filepath.Join(agents, "code-reviewer.md")
				want, err := Agents.ReadFile("sidecar/agents/code-reviewer.md")
				require.NoError(t, err)
				old := []byte("operator content\r\n")
				if operation == "repair" {
					old = []byte{}
				}
				if operation != "create" {
					require.NoError(t, os.MkdirAll(agents, 0o700))
					require.NoError(t, os.WriteFile(path, old, 0o644))
				}
				fileSync, dirSync := syncSeedFile, syncSeedDestination
				t.Cleanup(func() { syncSeedFile, syncSeedDestination = fileSync, dirSync })
				injected := errors.New("injected seed sync failure")
				var staged *os.File
				directoryCalled := false
				syncSeedFile = func(f *os.File) error {
					// Global source, skills and manifest must never use this seam.
					assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
					info, err := f.Stat()
					require.NoError(t, err)
					if runtime.GOOS != "windows" {
						assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "chmod must precede payload sync")
					}
					if strings.HasPrefix(filepath.Base(f.Name()), "code-reviewer.md.seed-") {
						staged = f
						data, err := os.ReadFile(f.Name())
						require.NoError(t, err)
						assert.Equal(t, want, data)
						if operation == "create" {
							assert.NoFileExists(t, path)
						} else {
							data, err = os.ReadFile(path)
							require.NoError(t, err)
							assert.Equal(t, old, data)
						}
						if failure == "file-sync" {
							return injected
						}
					}
					return fileSync(f)
				}
				syncSeedDestination = func(got layout.Policy, target string) error {
					assert.Equal(t, p, got)
					if target == path {
						directoryCalled = true
						require.NotNil(t, staged)
						_, err := staged.Stat()
						assert.ErrorIs(t, err, os.ErrClosed)
						data, err := os.ReadFile(path)
						require.NoError(t, err)
						assert.Equal(t, want, data)
						if failure == "directory-sync" {
							return injected
						}
					}
					return dirSync(got, target)
				}
				r, err := SeedVersion(dest, skills, agents, "test", true, WithRepositoryAgents(p))
				require.NotNil(t, staged, "real seed entrypoint must reach payload sync")
				if failure == "none" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					assert.Contains(t, strings.Join(r.Errors, "\n"), injected.Error())
				}
				bucket := r.Deployed
				if operation == "replace" {
					bucket = r.Updated
				} else if operation == "repair" {
					bucket = r.Repaired
				}
				assert.Equal(t, failure != "file-sync", directoryCalled)
				if failure == "file-sync" {
					assert.NotContains(t, bucket, path)
					assert.NotContains(t, strings.Join(r.Errors, "\n"), "change applied")
					if operation == "create" {
						assert.NoFileExists(t, path)
					} else {
						data, err := os.ReadFile(path)
						require.NoError(t, err)
						assert.Equal(t, old, data)
					}
				} else {
					assert.Contains(t, bucket, path, "applied bytes must not be reported unchanged")
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, want, data)
					if failure == "directory-sync" {
						assert.Contains(t, strings.Join(r.Errors, "\n"), "crash durability unconfirmed")
					}
				}
				mf, err := loadManifest(dest)
				require.NoError(t, err)
				_, tracked := mf.Entries[filepath.ToSlash(path)]
				assert.Equal(t, failure != "file-sync", tracked)
				entries, err := os.ReadDir(p.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries)
			})
		}
	}
}

func TestRepositorySeedDurabilityRetry(t *testing.T) {
	store, checkout := physicalTempDir(t), physicalTempDir(t)
	p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
	require.NoError(t, err)
	global := t.TempDir()
	dest, skills := filepath.Join(global, "ethos"), filepath.Join(global, "skills")
	agents := filepath.Join(checkout, ".claude", "agents")
	embedded, err := Agents.ReadDir("sidecar/agents")
	require.NoError(t, err)
	wants := make(map[string][]byte)
	for _, entry := range embedded {
		if entry.IsDir() || entry.Name() == "README.md" || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := Agents.ReadFile("sidecar/agents/" + entry.Name())
		require.NoError(t, err)
		wants[filepath.Join(agents, entry.Name())] = data
	}
	require.NotEmpty(t, wants)
	fileSync, dirSync := syncSeedFile, syncSeedDestination
	t.Cleanup(func() { syncSeedFile, syncSeedDestination = fileSync, dirSync })
	injected := errors.New("persistent agent directory sync failure")
	fault := true
	fileCalls := 0
	dirCalls := make(map[string]int)
	syncSeedFile = func(f *os.File) error {
		fileCalls++
		assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
		return fileSync(f)
	}
	syncSeedDestination = func(got layout.Policy, path string) error {
		dirCalls[path]++
		assert.Equal(t, p, got)
		want, ok := wants[path]
		require.True(t, ok, "only repository agents may use the sync seam")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, data)
		// Every agent fails: a later install cannot incidentally sync the
		// directory on behalf of an earlier applied-but-unconfirmed agent.
		if fault {
			return injected
		}
		return dirSync(got, path)
	}
	r, err := SeedVersion(dest, skills, agents, "test", false, WithRepositoryAgents(p))
	require.Error(t, err)
	assert.Len(t, r.Errors, len(wants))
	installed := make(map[string]os.FileInfo)
	for path := range wants {
		assert.Equal(t, 1, dirCalls[path])
		assert.Contains(t, r.Deployed, path)
		assert.NotContains(t, r.Unchanged, path)
		info, err := os.Stat(path)
		require.NoError(t, err)
		installed[path] = info
	}
	for attempt, persist := range []bool{true, false} {
		fault = persist
		r, err = SeedVersion(dest, skills, agents, "test", false, WithRepositoryAgents(p))
		if persist {
			assert.Error(t, err)
			assert.Len(t, r.Errors, len(wants))
		} else {
			assert.NoError(t, err)
			assert.Empty(t, r.Errors)
		}
		assert.Empty(t, r.Deployed)
		assert.Empty(t, r.Updated)
		assert.Empty(t, r.Repaired)
		assert.Equal(t, len(wants), fileCalls, "matching agents must not be rewritten")
		for path, want := range wants {
			assert.Equal(t, attempt+2, dirCalls[path])
			assert.Contains(t, r.Unchanged, path, "matching bytes remain unchanged even when confirmation fails")
			if persist {
				assert.Contains(t, r.Errors, "confirming "+path+": "+(&layout.InstallDurabilityError{Path: path, Err: injected}).Error())
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, want, data)
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.True(t, os.SameFile(installed[path], info))
		}
	}
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestGlobalSeedDoesNotUseRepositorySync(t *testing.T) {
	fileSync, dirSync := syncSeedFile, syncSeedDestination
	t.Cleanup(func() { syncSeedFile, syncSeedDestination = fileSync, dirSync })
	syncSeedFile = func(*os.File) error { t.Fatal("global payload sync changed"); return nil }
	syncSeedDestination = func(layout.Policy, string) error { t.Fatal("global directory sync changed"); return nil }
	root := t.TempDir()
	agents := filepath.Join(root, "agents")
	_, err := SeedVersion(filepath.Join(root, "ethos"), filepath.Join(root, "skills"), agents, "test", false)
	require.NoError(t, err)
	_, err = SeedVersion(filepath.Join(root, "ethos"), filepath.Join(root, "skills"), agents, "next", true)
	require.NoError(t, err)
}
