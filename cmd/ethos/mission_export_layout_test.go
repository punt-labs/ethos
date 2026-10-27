package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// TestMissionExportDefaultPathsAreBlameTier pins DES-079: plain `ethos mission
// export` at its default directory writes class-2 files.
func TestMissionExportDefaultPathsAreBlameTier(t *testing.T) {
	contract, result := missionExportPaths(missionExportDefaultDir, "m-2026-10-24-001")
	for _, p := range []string{contract, result} {
		c, e, ok := layout.Classify(p)
		if !ok || c != layout.Blame {
			t.Errorf("Classify(%q) = %v, %q, %v; want blame", p, c, e.Pattern, ok)
		}
	}
}

// TestMissionExportDefaultDirIsRepoRelative: the default export directory is
// under the repository root, not the current directory, so running the command
// from a subdirectory does not write a second .punt-labs tree there.
func TestMissionExportDefaultDirIsRepoRelative(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	home := t.TempDir()
	seedEvaluator(t, filepath.Join(home, ".punt-labs", "ethos"))
	chdirToFakeRepo(t, home)
	sub := filepath.Join(home, "internal")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	contract := filepath.Join(t.TempDir(), "contract.yaml")
	require.NoError(t, os.WriteFile(contract, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-g3g3
write_set:
  - cmd/ethos/mission.go
success_criteria:
  - make check passes
budget:
  rounds: 1
`), 0o600))
	env := append(os.Environ(), "HOME="+home)

	create := exec.Command(ethosBinary, "mission", "create", "--file", contract)
	create.Env = env
	require.NoError(t, create.Run())

	list := exec.Command(ethosBinary, "mission", "list", "--json")
	list.Env = env
	var out bytes.Buffer
	list.Stdout = &out
	require.NoError(t, list.Run())
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &entries))
	require.Len(t, entries, 1)
	id, _ := entries[0]["mission_id"].(string)
	require.NotEmpty(t, id)

	export := exec.Command(ethosBinary, "mission", "export", id)
	export.Env = env
	export.Dir = sub
	require.NoError(t, export.Run())

	assert.FileExists(t, filepath.Join(home, ".punt-labs", "ethos", "missions", id+".contract.yaml"))
	_, err := os.Stat(filepath.Join(sub, ".punt-labs"))
	assert.True(t, os.IsNotExist(err), "export from a subdirectory wrote a .punt-labs tree there")
}

// TestMissionExportDirectoryNamesTheCause: with no --dir and no repository
// root, the error says why, not just that there is none.
func TestMissionExportDirectoryNamesTheCause(t *testing.T) {
	t.Setenv("ETHOS_REPO_ROOT", filepath.Join(t.TempDir(), "nope"))
	_, err := missionExportDirectory("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ETHOS_REPO_ROOT")
	assert.Contains(t, err.Error(), "pass --dir")
}
