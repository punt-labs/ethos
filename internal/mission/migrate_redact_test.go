package mission

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/redact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestMigrateMission_RedactsLegacyRecords: migrate copies records written
// by an older ethos from the untracked global tree into the tracked repo
// tree, so it must not republish what those records leaked. The copy is
// structural (YAML scalars, JSON strings), so a credential value can never
// swallow a closing quote and corrupt the file.
func TestMigrateMission_RedactsLegacyRecords(t *testing.T) {
	f := newLeakFixture(t)
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-001"

	q := yaml.Node{Kind: yaml.ScalarNode, Style: yaml.DoubleQuotedStyle, Value: f.text}
	quoted, err := yaml.Marshal(&q)
	require.NoError(t, err)
	hash := strings.Repeat("ab", 32)
	legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml": []byte("mission_id: " + id + "\nstatus: open\nevaluator:\n  hash: " + hash +
			"\ncontext: " + string(quoted)),
		".jsonl":            []byte(`{"event":"abandon","details":{"reason":"TOKEN=abc"}}` + "\n"),
		".results.yaml":     []byte("- round: 1\n  prose: " + string(quoted)),
		".reflections.yaml": []byte("- round: 1\n  reason: " + string(quoted)),
	})
	repoAuditWithMission(t, repoRoot, "sess-1", id)

	var out bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, repoRoot, "", false, &out))

	dir := filepath.Join(repoRoot, ".punt-labs", "ethos", "missions", id)
	for _, name := range []string{"contract.yaml", "log.jsonl", "results.yaml", "reflections.yaml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, name)
		for _, raw := range append([]string{"TOKEN=abc"}, f.raw...) {
			assert.NotContains(t, string(data), raw, "%s leaks %q", name, raw)
		}
	}

	var doc map[string]any
	data, err := os.ReadFile(filepath.Join(dir, "contract.yaml"))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &doc), "a redacted copy must still parse")
	assert.Equal(t, id, doc["mission_id"])
	assert.Equal(t, hash, doc["evaluator"].(map[string]any)["hash"])

	log, err := os.ReadFile(filepath.Join(dir, "log.jsonl"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"event":"abandon","details":{"reason":"TOKEN=[redacted-secret]"}}`, string(log))
}

func TestMigrateMission_RefusesMalformedLegacyRecord(t *testing.T) {
	newLeakFixture(t)
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-002"
	legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml":  []byte("mission_id: " + id + "\n"),
		".jsonl": []byte("not json TOKEN=abc\n"),
	})
	repoAuditWithMission(t, repoRoot, "sess-2", id)

	var out bytes.Buffer
	require.Error(t, MigrateMission(globalRoot, repoRoot, repoRoot, "", false, &out),
		"a record that cannot be parsed cannot be redacted, so it is not copied")
	_, err := os.Stat(filepath.Join(repoRoot, ".punt-labs", "ethos", "missions", id))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(globalRoot, "missions", id+".yaml"))
	assert.NoError(t, err, "legacy side stays intact")
}

// TestRedactYAMLDoc_RefusesMultipleDocuments: yaml.Unmarshal into a Node
// reads the first document only, so re-encoding would drop the rest.
func TestRedactYAMLDoc_RefusesMultipleDocuments(t *testing.T) {
	id := func(s string) string { return s }
	_, _, err := redactYAMLDoc(id, []byte("a: 1\n---\nb: /home/alice/x\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple")

	out, changed, err := redactYAMLDoc(id, []byte("a: 1\n"))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, "a: 1\n", string(out))
}

func TestRedactYAMLDocPaths_EnforcementKeysGetOnlyPaths(t *testing.T) {
	in := "tools:\n    - Bash(gh auth token:*)\n    - Read(/home/alice/x)\ncontext: \"GITHUB_TOKEN=abc12345 a@b.org\"\n"
	out, changed, err := redactYAMLDocPaths(redact.Prose, func(s string) string { return redact.HomePaths(s) }, []byte(in))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Contains(t, string(out), "Bash(gh auth token:*)")
	assert.Contains(t, string(out), "Read(~/x)")
	assert.Contains(t, string(out), "GITHUB_TOKEN="+redact.SecretToken)
	assert.Contains(t, string(out), redact.EmailToken)
}

// TestCopyIfExists_TornLogLineErrorIsActionable: the error names the file
// and line and says what to do about it. MigrateMission prints it to
// stderr per mission and returns only a count.
func TestCopyIfExists_TornLogLineErrorIsActionable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "m-2026-05-22-003.jsonl")
	require.NoError(t, os.WriteFile(src, []byte(`{"event":"create"}`+"\n"+`{"event":"clo`+"\n"), 0o600))
	r := PathRedactor{}

	err := copyIfExists(r, src, filepath.Join(dir, "log.jsonl"))
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, src)
	assert.Contains(t, msg, "line 2")
	assert.Contains(t, msg, "fix or remove the line and re-run `ethos mission migrate`")
}

// TestMigrateMission_ContractEnforcementFieldsKeepTheirValue: the copy
// applies the same path-only rule the contract writer does.
func TestMigrateMission_ContractEnforcementFieldsKeepTheirValue(t *testing.T) {
	newLeakFixture(t)
	globalRoot := t.TempDir()
	repoRoot := t.TempDir()
	id := "m-2026-05-22-004"
	legacyMissionFiles(t, globalRoot, id, map[string][]byte{
		".yaml": []byte("mission_id: " + id + "\ntools:\n    - Bash(gh auth token:*)\n"),
	})
	repoAuditWithMission(t, repoRoot, "sess-4", id)

	var out bytes.Buffer
	require.NoError(t, MigrateMission(globalRoot, repoRoot, repoRoot, "", false, &out))
	data, err := os.ReadFile(filepath.Join(repoRoot, ".punt-labs", "ethos", "missions", id, "contract.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "Bash(gh auth token:*)")
}
