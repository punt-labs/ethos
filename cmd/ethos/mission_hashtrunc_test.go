package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A YAML comment opens at an unquoted '#', so a free-text value citing a
// PR or bead number is recorded shortened and every downstream validator
// still passes. internal/mission owns the detection; these tests own the
// wiring — that each command a human's file enters through actually
// calls it, and that nothing is persisted when it fires.

// writeTruncatedContractFile drops a contract whose first success
// criterion is cut short at an unquoted '#'.
func writeTruncatedContractFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "contract.yaml")
	body := `leader: claude
worker: bwk
evaluator:
  handle: djb
write_set:
  - internal/mission/
success_criteria:
  - PR #515 merges with 6/6 checks green
budget:
  rounds: 3
  reflection_after_each: true
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestMissionCreate_RefusesHashTruncatedCriterion(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeTruncatedContractFile(t)

	err := runMissionCreate()
	require.Error(t, err, "create accepted a success criterion truncated at an unquoted '#'")
	assert.Contains(t, err.Error(), "success_criteria[0]")
	assert.Contains(t, err.Error(), `"PR"`)

	ids, listErr := missionStore().List()
	require.NoError(t, listErr)
	assert.Empty(t, ids, "a refused contract must not be persisted")
}

func TestMissionLint_RefusesHashTruncatedCriterion(t *testing.T) {
	missionTestEnv(t)

	err := runMissionLint(writeTruncatedContractFile(t))
	require.Error(t, err, "lint accepted a success criterion truncated at an unquoted '#'")
	assert.Contains(t, err.Error(), "success_criteria[0]")
}

func TestMissionResult_RefusesHashTruncatedEvidenceName(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdoutE(t, func() error { return runMissionCreate() })

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	// The exact line that shipped on PR #516.
	dir := t.TempDir()
	path := filepath.Join(dir, "result.yaml")
	body := fmt.Sprintf(`mission: %s
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: PR #515 merged as 6f61406 — 6/6 CI checks green, 17 review threads resolved
    status: pass
`, id)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	missionResultFile = path

	err = runMissionResult(id, path)
	require.Error(t, err, "result accepted an evidence name truncated at an unquoted '#'")
	assert.Contains(t, err.Error(), "evidence[0].name")
	assert.Contains(t, err.Error(), `"PR"`)

	loaded, loadErr := ms.LoadResult(id, 1)
	if loadErr == nil {
		assert.Nil(t, loaded, "a refused result must not be persisted")
	}
}
