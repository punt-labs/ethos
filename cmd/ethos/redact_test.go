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

// gitRepoWith creates a git repo in a fresh directory holding files, all
// tracked, and makes it the working directory.
func gitRepoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q")
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		run("add", "-f", rel)
	}
	t.Chdir(dir)
	return dir
}

func TestMissionScrub(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ETHOS_REPO_ROOT", "")
	t.Cleanup(func() { missionScrubApply = false })
	const rel = ".punt-labs/ethos/missions/m-2026-01-01-001/reflections.yaml"
	const gone = ".punt-labs/ethos/missions/m-2026-01-01-001/results.yaml"
	body := "reflections:\n    - reason: \"at " + filepath.Join(home, "y") + "\"\n"
	dir := gitRepoWith(t, map[string]string{rel: body, gone: "results: []\n"})
	require.NoError(t, os.Remove(filepath.Join(dir, gone)))

	// Dry run lists and writes nothing.
	missionScrubApply = false
	stdout, stderr, err := execHandler(t, "mission", "scrub")
	require.NoError(t, err)
	assert.Contains(t, stdout, "would rewrite "+rel)
	assert.Contains(t, stdout, "dry run")
	assert.Contains(t, stderr, "skipping "+gone)
	got, err := os.ReadFile(filepath.Join(dir, rel))
	require.NoError(t, err)
	assert.Equal(t, body, string(got))

	// --apply rewrites; a second run has nothing to do.
	stdout, _, err = execHandler(t, "mission", "scrub", "--apply")
	require.NoError(t, err)
	assert.Contains(t, stdout, "rewrote "+rel)
	got, err = os.ReadFile(filepath.Join(dir, rel))
	require.NoError(t, err)
	assert.NotContains(t, string(got), home)
	assert.Contains(t, string(got), "at ~/y")

	missionScrubApply = false
	stdout, _, err = execHandler(t, "mission", "scrub")
	require.NoError(t, err)
	assert.Contains(t, stdout, "nothing to change")
}

func TestMissionScrub_FailureIsNonZeroAndOthersStillScrubbed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ETHOS_REPO_ROOT", "")
	t.Cleanup(func() { missionScrubApply = false })
	const bad = ".punt-labs/ethos/missions/m-2026-01-01-001/contract.yaml"
	const good = ".punt-labs/ethos/missions/m-2026-01-01-001/delegations/d-1/prompt.md"
	dir := gitRepoWith(t, map[string]string{bad: "a: [\n", good: "read " + filepath.Join(home, "n") + "\n"})

	_, _, err := execHandler(t, "mission", "scrub", "--apply")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 rewritten, 1 failed")
	got, err := os.ReadFile(filepath.Join(dir, good))
	require.NoError(t, err)
	assert.Equal(t, "read ~/n\n", string(got))
}
