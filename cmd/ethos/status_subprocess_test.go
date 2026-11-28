//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLI_StatusNamesDecidingEnablementMarker(t *testing.T) {
	const (
		project = ".punt-labs/ethos/enabled"
		local   = ".punt-labs/local/ethos/enabled"
		disable = ".punt-labs/local/ethos/disabled"
		optOut  = ".punt-labs/ethos/opt-out"
	)
	for _, tc := range []struct {
		name     string
		files    []string
		enabled  bool
		deciding string
	}{
		{name: "absent"},
		{name: "shared", files: []string{project}, enabled: true, deciding: project},
		{name: "personal", files: []string{local}, enabled: true, deciding: local},
		{name: "locally disabled", files: []string{project, local, disable}, deciding: disable},
		{name: "opted out", files: []string{project, local, disable, optOut}, deciding: optOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			se := freshCLIEnv(t, "status-human")
			for _, rel := range tc.files {
				path := filepath.Join(se.repo, rel)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, nil, 0o644))
			}
			stdout, stderr, code := runCLI(t, se, "status", "--json")
			require.Zero(t, code, stderr)
			var result struct {
				Enabled      bool   `json:"enabled"`
				DecidingFile string `json:"deciding_file"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &result), stdout)
			assert.Equal(t, tc.enabled, result.Enabled)
			assert.Equal(t, tc.deciding, result.DecidingFile)

			stdout, _, _ = runCLI(t, se, "doctor", "--json")
			var checks []struct{ Name, Status, Detail string }
			require.NoError(t, json.Unmarshal([]byte(stdout), &checks), stdout)
			detail := "ethos is off: no enablement marker"
			if tc.deciding != "" {
				state := "off"
				if tc.enabled {
					state = "on"
				}
				detail = "ethos is " + state + ": " + tc.deciding
			}
			assert.Contains(t, checks, struct{ Name, Status, Detail string }{"Effective enablement", "PASS", detail})
		})
	}
}
