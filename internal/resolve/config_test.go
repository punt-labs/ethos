package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestConfigRecordingMode(t *testing.T) {
	for _, rel := range []string{".punt-labs/ethos.yaml", ".punt-labs/ethos/config.yaml"} {
		for _, tt := range []struct {
			name, body, record string
			mode               layout.RecordMode
		}{
			{"empty config", "", "", layout.RecordBlame},
			{"unset", "agent: bwk\n", "", layout.RecordBlame},
			{"empty string", "record: ''\n", "", layout.RecordBlame},
			{"blame", "record: blame\n", RecordBlame, layout.RecordBlame},
			{"full", "record: full\n", RecordFull, layout.RecordFull},
			{"alias", "mode: &mode full\nrecord: *mode\n", RecordFull, layout.RecordFull},
			{"merge", "defaults: &defaults {record: full}\n<<: *defaults\n", RecordFull, layout.RecordFull},
		} {
			t.Run(rel+"/"+tt.name, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, filepath.FromSlash(rel))
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(tt.body), 0o600))
				cfg, err := ConfigOf(root)
				require.NoError(t, err)
				require.NotNil(t, cfg)
				assert.Equal(t, tt.record, cfg.Record)
				mode, err := cfg.RecordingMode()
				require.NoError(t, err)
				assert.Equal(t, tt.mode, mode)
			})
		}
	}
}

func TestConfigRejectsInvalidRecordingMode(t *testing.T) {
	for _, body := range []string{
		"record: unknown\n", "record: Full\n", "record: ' full '\n",
		"record: null\n", "record:\n", "record: ~\n",
		"record: true\n", "record: 1\n", "record: 0.0\n",
		"record: [full]\n", "record: {mode: full}\n",
		"mode: &mode false\nrecord: *mode\n",
		"defaults: &defaults {record: null}\n<<: *defaults\n",
		"record: full\nrecord: blame\n",
	} {
		t.Run(body, func(t *testing.T) {
			for _, rel := range []string{".punt-labs/ethos.yaml", ".punt-labs/ethos/config.yaml"} {
				root := t.TempDir()
				path := filepath.Join(root, filepath.FromSlash(rel))
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
				cfg, err := ConfigOf(root)
				require.ErrorContains(t, err, "record")
				assert.Contains(t, err.Error(), path)
				assert.Nil(t, cfg)
			}
		})
	}
}

func TestConfigNewPathFailureDoesNotUseLegacyMode(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("record: invalid\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos", "config.yaml"), []byte("record: full\n"), 0o600))
	cfg, err := ConfigOf(root)
	require.Error(t, err)
	assert.Nil(t, cfg)
}

func TestConfigMarshalValidatesAndPreservesFields(t *testing.T) {
	for _, record := range []string{"", RecordBlame, RecordFull} {
		cfg := RepoConfig{
			Agent: "bwk", Team: "engineering", ActiveBundle: "bundle",
			Resolution: ResolutionRepoOnly, Record: record, MaxDelegationDepth: 4,
		}
		data, err := yaml.Marshal(cfg)
		require.NoError(t, err)
		if record == "" {
			assert.NotContains(t, string(data), "record:")
		} else {
			assert.Contains(t, string(data), "record: "+record)
		}
		var roundTrip RepoConfig
		require.NoError(t, yaml.Unmarshal(data, &roundTrip))
		assert.Equal(t, cfg, roundTrip)
	}
	cfg := RepoConfig{Record: "invalid"}
	_, err := yaml.Marshal(cfg)
	assert.ErrorContains(t, err, "record")
	_, err = yaml.Marshal(&cfg)
	assert.ErrorContains(t, err, "record")
	_, err = cfg.RecordingMode()
	assert.Error(t, err)
}

func TestConfigInvalidDecodePreservesPriorValue(t *testing.T) {
	cfg := RepoConfig{Agent: "bwk", Record: RecordFull}
	before := cfg
	err := yaml.Unmarshal([]byte("agent: other\nrecord: invalid\n"), &cfg)
	require.Error(t, err)
	assert.Equal(t, before, cfg)
}
