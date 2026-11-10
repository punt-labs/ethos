package autorepair

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestConfigFileStep(t *testing.T) {
	cases := []struct {
		name      string
		fixture   string
		prepare   func(t *testing.T, repo string) // optional change before the run
		items     int
		wantAgent string // content of .punt-labs/ethos.yaml afterwards, "" = absent
	}{
		{
			name:    "only legacy config",
			fixture: "corpus/v4.17.0",
			prepare: func(t *testing.T, repo string) {
				require.NoError(t, os.Remove(filepath.Join(repo, ".punt-labs", "ethos.yaml")))
				s81113Write(t, filepath.Join(repo, ".punt-labs", "ethos", "config.yaml"), "agent: legacy\n")
			},
			items:     1,
			wantAgent: "agent: legacy\n",
		},
		{
			name:      "both files keep the new",
			fixture:   "damage/both-config-files",
			items:     1,
			wantAgent: "agent: wperson\n",
		},
		{name: "current v4.17.0", fixture: "corpus/v4.17.0", items: 0, wantAgent: "*"},
		{name: "current v4.22.1", fixture: "corpus/v4.22.1", items: 0, wantAgent: "*"},
		{name: "v3.11.0", fixture: "corpus/v3.11.0", items: 0, wantAgent: "*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := s81113Copy(t, tc.fixture)
			if tc.prepare != nil {
				tc.prepare(t, tr.Repo)
			}
			newPath := filepath.Join(tr.Repo, ".punt-labs", "ethos.yaml")
			oldPath := filepath.Join(tr.Repo, ".punt-labs", "ethos", "config.yaml")
			before := map[bool][]byte{}
			if tc.wantAgent == "*" {
				before[true] = s81113Read(t, newPath)
			}

			out := s81113Run(t, "config-file", tr.Env(t))
			assert.Equal(t, tc.items, out.Items)
			assert.False(t, out.Deferred)
			assert.False(t, s81113Exists(oldPath), "legacy config must be gone")
			if tc.wantAgent == "*" {
				assert.Equal(t, before[true], s81113Read(t, newPath), "current config bytes unchanged")
			} else {
				assert.Equal(t, tc.wantAgent, string(s81113Read(t, newPath)))
			}
			if tc.items > 0 {
				require.NotEmpty(t, tr.Log.entries)
				for _, e := range tr.Log.entries {
					assert.Equal(t, "config-file", e.step)
					assert.NoError(t, e.err)
				}
			} else {
				assert.Empty(t, tr.Log.entries)
			}

			snap := s81113Snapshot(t, tr)
			again := s81113Run(t, "config-file", tr.Env(t))
			assert.Equal(t, 0, again.Items, "second run changes nothing")
			assert.Equal(t, snap, s81113Snapshot(t, tr), "second run leaves the tree identical")
		})
	}
}

func TestConfigFileStepSyncsDirectories(t *testing.T) {
	for _, newExists := range []bool{false, true} {
		name := "rename"
		if newExists {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			tr := s81113Copy(t, "damage/both-config-files")
			newPath := filepath.Join(tr.Repo, ".punt-labs", "ethos.yaml")
			if !newExists {
				require.NoError(t, os.Remove(newPath))
			}
			var synced []string
			orig := syncConfigDirs
			syncConfigDirs = func(_ layout.Policy, p string) error {
				synced = append(synced, filepath.Dir(p))
				return nil
			}
			t.Cleanup(func() { syncConfigDirs = orig })

			out := s81113Run(t, "config-file", tr.Env(t))
			require.Equal(t, 1, out.Items)
			assert.Contains(t, synced, filepath.Join(tr.Repo, ".punt-labs", "ethos"), "the directory that lost config.yaml is synced")
			if !newExists {
				assert.Contains(t, synced, filepath.Join(tr.Repo, ".punt-labs"), "the directory that gained ethos.yaml is synced")
			}
		})
	}
}

func TestConfigFileStepLeavesNonRegularNew(t *testing.T) {
	tr := s81113Copy(t, "damage/both-config-files")
	newPath := filepath.Join(tr.Repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.Remove(newPath))
	require.NoError(t, os.Symlink("does-not-exist", newPath))
	oldPath := filepath.Join(tr.Repo, ".punt-labs", "ethos", "config.yaml")

	out := s81113Run(t, "config-file", tr.Env(t))
	assert.Equal(t, 0, out.Items)
	assert.True(t, s81113Exists(oldPath), "legacy config is kept when the new path is not a regular file")
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "config-file", out.Residuals[0].Step)
}
