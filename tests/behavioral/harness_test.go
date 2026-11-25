package behavioral

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFixtureMissionCreation(t *testing.T) {
	f := SetupFixture(t)
	id := f.CreateMission(t, `leader: test-leader
worker: test-agent
evaluator:
  handle: test-evaluator
write_set:
  - pkg/counter/counter.go
success_criteria:
  - verify the counter
budget:
  rounds: 2
  reflection_after_each: true
`)
	require.Equal(t, "implement", f.MissionShow(t, id)["type"])
	require.Empty(t, f.MissionResults(t, id))
	base := f.CommitMissionFixture(t, id)

	result := filepath.Join(f.Root, "result.yaml")
	writeFile(t, result, "mission: "+id+"\n"+`round: 1
author: test-agent
verdict: pass
confidence: 1
files_changed: []
evidence:
  - name: verify the counter
    status: pass
`)
	_, stderr, err := f.runEthos(t, "mission", "result", id, "--file", result)
	require.NoError(t, err, stderr)
	results := f.MissionResults(t, id)
	require.Len(t, results, 1)
	require.Equal(t, "pass", results[0]["verdict"])
	require.Equal(t, []string{missionResultPath(id)}, f.GitDiffFiles(t, base))

	f.Reflect(t, id, "mission: "+id+"\n"+`round: 1
author: test-leader
converging: true
signals:
  - verified the counter
recommendation: continue
`)
	reflections := f.MissionReflections(t, id)
	require.Len(t, reflections, 1)
	require.NotNil(t, reflections[0].Decision)
	require.Equal(t, "continue", reflections[0].Decision.Recommendation)
	require.Equal(t, float64(2), f.Advance(t, id)["current_round"])
}

func TestFixtureMCPStartupKeepsWorktreeClean(t *testing.T) {
	f := SetupFixture(t)
	base := f.GitHead(t)
	_, stderr, err := f.runEthos(t, "serve")
	require.NoError(t, err, stderr)
	require.Empty(t, f.GitDiffFiles(t, base))
}

func TestResolveEthosBinary(t *testing.T) {
	t.Run("uses executable ETHOS_BINARY without building", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "ethos")
		require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))

		built := false
		got, err := resolveEthosBinary(bin, func() (string, error) {
			built = true
			return "", errors.New("should not build")
		})
		require.NoError(t, err)
		assert.Equal(t, bin, got)
		assert.False(t, built)
	})

	t.Run("rejects missing ETHOS_BINARY", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		_, err := resolveEthosBinary(missing, func() (string, error) {
			return "/unused", nil
		})
		require.Error(t, err)
	})

	t.Run("rejects directory ETHOS_BINARY", func(t *testing.T) {
		_, err := resolveEthosBinary(t.TempDir(), func() (string, error) {
			return "/unused", nil
		})
		require.Error(t, err)
	})

	t.Run("rejects non-executable ETHOS_BINARY", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "ethos")
		require.NoError(t, os.WriteFile(bin, []byte("not executable"), 0o600))

		_, err := resolveEthosBinary(bin, func() (string, error) {
			return "/unused", nil
		})
		require.Error(t, err)
	})

	t.Run("builds when ETHOS_BINARY is empty", func(t *testing.T) {
		built := false
		got, err := resolveEthosBinary("", func() (string, error) {
			built = true
			return "/built/ethos", nil
		})
		require.NoError(t, err)
		assert.Equal(t, "/built/ethos", got)
		assert.True(t, built)
	})
}
