//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestRedactedYAML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	secret := "ghp_" + strings.Repeat("a1B2", 9)
	text := "at " + filepath.Join(home, "x.go") + " jane@example.com " + secret

	c := mission.Contract{MissionID: "m-2026-04-08-001", Context: text, Evaluator: mission.Evaluator{Hash: strings.Repeat("ab", 32)}}
	data, err := mission.RedactedYAML("", c)
	require.NoError(t, err)
	for _, raw := range []string{home, "jane@example.com", secret} {
		assert.NotContains(t, string(data), raw)
	}
	assert.Contains(t, string(data), strings.Repeat("ab", 32))
	assert.Contains(t, string(data), "m-2026-04-08-001")

	t.Setenv("HOME", "/")
	_, err = mission.RedactedYAML("", c)
	assert.Error(t, err, "export must refuse when no redactor can be built")
}

// TestMissionExport_RedactsLegacyRecords plants an unredacted contract
// and result on disk, as an older ethos wrote them, and checks that
// export does not carry the leak into the exported files.
func TestMissionExport_RedactsLegacyRecords(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}
	home := t.TempDir()
	seedEvaluator(t, filepath.Join(home, ".punt-labs", "ethos"))
	chdirToFakeRepo(t, home)
	tmp := t.TempDir()
	env := append(os.Environ(), "HOME="+home)

	contract := filepath.Join(tmp, "contract.yaml")
	require.NoError(t, os.WriteFile(contract, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-k28.7
write_set:
  - cmd/ethos/mission.go
success_criteria:
  - LEAKPLACEHOLDER
budget:
  rounds: 2
`), 0o600))
	cmd := exec.Command(ethosBinary, "mission", "create", "--file", contract)
	cmd.Env = env
	require.NoError(t, cmd.Run())

	listCmd := exec.Command(ethosBinary, "mission", "list", "--json")
	listCmd.Env = env
	var listOut bytes.Buffer
	listCmd.Stdout = &listOut
	require.NoError(t, listCmd.Run())
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(listOut.Bytes(), &entries))
	require.Len(t, entries, 1)
	id, _ := entries[0]["mission_id"].(string)
	require.NotEmpty(t, id)

	secret := "ghp_" + strings.Repeat("a1B2", 9)
	leak := filepath.Join(home, "work") + " jane@example.com " + secret
	repo, err := os.Getwd()
	require.NoError(t, err)
	path := filepath.Join(repo, ".punt-labs", "ethos", "missions", id, "contract.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "contract must live in the repo tree")
	planted := strings.Replace(string(raw), "- LEAKPLACEHOLDER", "- '"+leak+"'", 1)
	require.NotEqual(t, string(raw), planted, "placeholder must be present to be replaced")
	require.NoError(t, os.WriteFile(path, []byte(planted), 0o600))

	exportDir := filepath.Join(tmp, "out")
	exportCmd := exec.Command(ethosBinary, "mission", "export", id, "--dir", exportDir)
	exportCmd.Env = env
	out, err := exportCmd.CombinedOutput()
	require.NoError(t, err, string(out))

	data, err := os.ReadFile(filepath.Join(exportDir, id+".contract.yaml"))
	require.NoError(t, err)
	for _, r := range []string{home, "jane@example.com", secret} {
		assert.NotContains(t, string(data), r)
	}
	assert.Contains(t, string(data), "<repo>/work", "the fake repo is the home directory, and <repo> wins")
}
