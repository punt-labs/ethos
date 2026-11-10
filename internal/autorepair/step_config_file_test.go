package autorepair

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

			again := s81113Run(t, "config-file", tr.Env(t))
			assert.Equal(t, 0, again.Items, "second run changes nothing")
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
