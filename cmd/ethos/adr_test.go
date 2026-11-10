//go:build linux || darwin

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

func adrCLIEnv(t *testing.T) *cliSubprocessEnv {
	t.Helper()
	se := setupCLISubprocessEnv(t)
	repo, err := filepath.EvalSymlinks(se.repo)
	require.NoError(t, err)
	se.repo = repo
	return se
}

func TestCLI_ADRRepositoryStorage(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "ADR acceptance requires the real CLI")
	for _, mode := range []string{"blame", "full"} {
		t.Run(mode, func(t *testing.T) {
			se := adrCLIEnv(t)
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), []byte("record: "+mode+"\n"), 0o600))
			legacy := adr.NewStore(filepath.Join(se.home, ".punt-labs", "ethos", "adrs"))
			for i := 0; i < 2; i++ {
				require.NoError(t, legacy.Create(&adr.ADR{Title: "Legacy decision", Context: se.home + "/private person@example.org password=synthetic", Decision: "Use YAML", Author: "bwk"}))
			}
			source, err := os.ReadFile(filepath.Join(legacy.Dir(), "DES-001.yaml"))
			require.NoError(t, err)
			stdout, stderr, exit := runCLI(t, se, "adr", "create", "--title", "Repository decision", "--decision", se.repo+"/design api_key=synthetic", "--author", "bwk", "--json")
			require.Equal(t, 0, exit, stderr)
			var created adr.ADR
			require.NoError(t, json.Unmarshal([]byte(stdout), &created))
			assert.Equal(t, "DES-003", created.ID)
			assert.NotContains(t, created.Decision, "synthetic")
			assert.Contains(t, created.Decision, "<repo>/design")
			record := filepath.Join(se.repo, ".punt-labs", "ethos", "adrs")
			assert.FileExists(t, filepath.Join(record, created.ID+".yaml"))
			assert.NoFileExists(t, filepath.Join(legacy.Dir(), created.ID+".yaml"))
			stdout, stderr, exit = runCLI(t, se, "adr", "show", "DES-001", "--json")
			require.Equal(t, 0, exit, stderr)
			assert.Contains(t, stdout, "Legacy decision", "legacy compatibility read")
			stdout, stderr, exit = runCLI(t, se, "adr", "settle", "DES-001", "--json")
			require.Equal(t, 0, exit, stderr)
			var settled adr.ADR
			require.NoError(t, json.Unmarshal([]byte(stdout), &settled))
			assert.Equal(t, adr.StatusSettled, settled.Status)
			assert.NotContains(t, settled.Context, se.home)
			assert.NotContains(t, settled.Context, "person@example.org")
			assert.NotContains(t, settled.Context, "synthetic")
			unchanged, err := os.ReadFile(filepath.Join(legacy.Dir(), "DES-001.yaml"))
			require.NoError(t, err)
			assert.Equal(t, source, unchanged)
			stdout, stderr, exit = runCLI(t, se, "adr", "list", "--json")
			require.Equal(t, 0, exit, stderr)
			var list []adr.ADR
			require.NoError(t, json.Unmarshal([]byte(stdout), &list))
			assert.Len(t, list, 3)
			assert.Equal(t, adr.StatusSettled, list[0].Status, "list resolves repository copy first")
			require.NoError(t, filepath.WalkDir(record, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				assert.NotContains(t, entry.Name(), ".lock", path)
				assert.NotContains(t, entry.Name(), ".ethos-tmp", path)
				return nil
			}))
			// A corrupt repository copy must not be masked by its valid global
			// source, nor silently written back to that source on settle.
			require.NoError(t, os.WriteFile(filepath.Join(record, "DES-001.yaml"), []byte("title: ["), 0o600))
			_, stderr, exit = runCLI(t, se, "adr", "show", "DES-001")
			assert.Equal(t, 1, exit)
			assert.Contains(t, stderr, "invalid ADR")
			_, stderr, exit = runCLI(t, se, "adr", "settle", "DES-001")
			assert.Equal(t, 1, exit)
			assert.Contains(t, stderr, "invalid ADR")
		})
	}
}

func TestCLI_ADROutsideRepositoryRemainsGlobal(t *testing.T) {
	require.NotEmpty(t, ethosBinary)
	se := adrCLIEnv(t)
	se.repo = t.TempDir()
	se.env = append(se.env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(se.repo))
	stdout, stderr, exit := runCLI(t, se, "adr", "create", "--title", "Global", "--decision", "contact person@example.org", "--author", "bwk", "--json")
	require.Equal(t, 0, exit, stderr)
	assert.Contains(t, stdout, "person@example.org", "standalone behavior is unchanged")
	assert.FileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "adrs", "DES-001.yaml"))
	assert.NoDirExists(t, filepath.Join(se.repo, ".punt-labs"))
}

func TestADRStoreUsesLocatedSnapshotAndRefusesErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	located := resolve.Located{Store: root, Checkout: root, Ethos: filepath.Join(root, ".punt-labs", "ethos"), Config: &resolve.RepoConfig{Record: "full"}}
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("record: invalid\n"), 0o600))
	prior := cmdRoots.Load()
	t.Cleanup(func() { cmdRoots.Store(prior) })
	cmdRoots.Store(&repoRoots{Located: located})
	s, err := adrStore()
	require.NoError(t, err, "constructor must not reread changed config")
	a := &adr.ADR{Title: "Snapshot", Decision: "Use selected policy"}
	require.NoError(t, s.Create(a))
	assert.Equal(t, filepath.Join(located.Ethos, "adrs"), s.Dir())
	for _, kind := range []string{"store", "checkout", "configuration"} {
		bad := located
		sentinel := errors.New("unusable " + kind)
		switch kind {
		case "store":
			bad.StoreErr = sentinel
		case "checkout":
			bad.CheckoutErr = sentinel
		case "configuration":
			bad.Config = &resolve.RepoConfig{Record: "invalid"}
		}
		cmdRoots.Store(&repoRoots{Located: bad, Global: true})
		_, err := adrStore()
		require.Error(t, err, "even a global-only command classification must not mask an ADR location failure")
		if kind != "configuration" {
			assert.True(t, errors.Is(err, sentinel))
		}
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(), a.ID+".yaml"))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(data), "Snapshot"))
}

func TestCLI_ADRRejectsMalformedRepositoryMetadata(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "metadata refusal must exercise the real CLI")
	for _, mode := range []string{"blame", "full"} {
		t.Run(mode, func(t *testing.T) {
			se := adrCLIEnv(t)
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), []byte("record: "+mode+"\n"), 0o600))
			record := filepath.Join(se.repo, ".punt-labs", "ethos", "adrs")
			for _, field := range []string{"--author", "--mission-id", "--bead-id"} {
				for _, value := range []string{"password=synthetic-only", "person@example.org"} {
					stdout, stderr, exit := runCLI(t, se, "adr", "create", "--title", "Refused", "--decision", "Do not store", field, value)
					assert.Equal(t, 1, exit)
					assert.NotContains(t, stdout+stderr, value, "refusal must not echo credentials or addresses")
					assert.NoDirExists(t, record)
				}
			}
			legacy := adr.NewStore(filepath.Join(se.home, ".punt-labs", "ethos", "adrs"))
			a := &adr.ADR{Title: "Historical author", Decision: "Retain global compatibility", Author: "person@example.org"}
			require.NoError(t, legacy.Create(a))
			source, err := os.ReadFile(filepath.Join(legacy.Dir(), a.ID+".yaml"))
			require.NoError(t, err)
			for _, args := range [][]string{
				{"adr", "settle", a.ID},
			} {
				stdout, stderr, exit := runCLI(t, se, args...)
				assert.Equal(t, 1, exit)
				assert.Contains(t, stderr, "author")
				assert.NotContains(t, stdout+stderr, a.Author)
				assert.NoDirExists(t, record)
				unchanged, err := os.ReadFile(filepath.Join(legacy.Dir(), a.ID+".yaml"))
				require.NoError(t, err)
				assert.Equal(t, source, unchanged)
			}
		})
	}
}
