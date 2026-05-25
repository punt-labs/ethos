//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findMissionsEnv sets up a temp repo with a .punt-labs/ethos/
// directory and resets the find-missions flag state between tests.
func findMissionsEnv(t *testing.T) (home, repo string) {
	t.Helper()
	home = t.TempDir()
	repo = filepath.Join(home, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	gitInitDir(t, repo, home)

	ethosDir := filepath.Join(repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosDir, 0o700))

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	resetFindMissionsFlags()
	t.Cleanup(resetFindMissionsFlags)
	return home, repo
}

// resetFindMissionsFlags zeroes the package-level flag variables and
// clears cobra's Changed state so tests do not leak into each other.
func resetFindMissionsFlags() {
	findMissionsSince = ""
	findMissionsWorker = ""
	findMissionsStatus = ""
	findMissionsFormat = "json"
	for _, name := range []string{"since", "worker", "status", "format"} {
		if f := findMissionsCmd.Flags().Lookup(name); f != nil {
			f.Changed = false
		}
	}
}

// seedMissionsJSONL writes TraceSummary lines to the repo's
// missions.jsonl and returns the file path.
func seedMissionsJSONL(t *testing.T, repo string, traces []mission.TraceSummary) string {
	t.Helper()
	dir := filepath.Join(repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "missions.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ts := range traces {
		require.NoError(t, enc.Encode(ts))
	}
	return path
}

// sampleTraces returns a set of TraceSummary values for testing.
func sampleTraces() []mission.TraceSummary {
	return []mission.TraceSummary{
		{
			ID:        "m-2026-05-20-001",
			CreatedAt: "2026-05-20T09:00:00Z",
			ClosedAt:  "2026-05-20T10:00:00Z",
			Status:    "closed",
			Leader:    "claude",
			Worker:    "bwk",
			Evaluator: "rsc",
			Verdict:   "pass",
		},
		{
			ID:        "m-2026-05-21-001",
			CreatedAt: "2026-05-21T11:00:00Z",
			ClosedAt:  "2026-05-21T12:00:00Z",
			Status:    "failed",
			Leader:    "claude",
			Worker:    "mdm",
			Evaluator: "rop",
			Verdict:   "fail",
		},
		{
			ID:        "m-2026-05-22-001",
			CreatedAt: "2026-05-22T14:00:00Z",
			ClosedAt:  "2026-05-22T15:00:00Z",
			Status:    "closed",
			Leader:    "claude",
			Worker:    "bwk",
			Evaluator: "djb",
			Verdict:   "pass",
		},
	}
}

func TestFindMissions_RoundTrip(t *testing.T) {
	_, repo := findMissionsEnv(t)
	traces := sampleTraces()
	seedMissionsJSONL(t, repo, traces)

	stdout, _, err := execHandler(t, "find", "missions")
	require.NoError(t, err)

	lines := splitNonEmpty(stdout)
	require.Len(t, lines, 3, "expected 3 JSONL lines, got %d", len(lines))

	var first mission.TraceSummary
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "m-2026-05-20-001", first.ID)
	assert.Equal(t, "bwk", first.Worker)
}

func TestFindMissions_SinceFilter(t *testing.T) {
	_, repo := findMissionsEnv(t)
	seedMissionsJSONL(t, repo, sampleTraces())

	stdout, _, err := execHandler(t, "find", "missions", "--since", "2026-05-21T00:00:00Z")
	require.NoError(t, err)

	lines := splitNonEmpty(stdout)
	require.Len(t, lines, 2, "since 05-21 should exclude the 05-20 trace")

	var first mission.TraceSummary
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "m-2026-05-21-001", first.ID)
}

func TestFindMissions_SinceDateOnly(t *testing.T) {
	_, repo := findMissionsEnv(t)
	seedMissionsJSONL(t, repo, sampleTraces())

	stdout, _, err := execHandler(t, "find", "missions", "--since", "2026-05-22")
	require.NoError(t, err)

	lines := splitNonEmpty(stdout)
	require.Len(t, lines, 1, "since 05-22 date-only should include only the 05-22 trace")

	var first mission.TraceSummary
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "m-2026-05-22-001", first.ID)
}

func TestFindMissions_WorkerFilter(t *testing.T) {
	_, repo := findMissionsEnv(t)
	seedMissionsJSONL(t, repo, sampleTraces())

	stdout, _, err := execHandler(t, "find", "missions", "--worker", "mdm")
	require.NoError(t, err)

	lines := splitNonEmpty(stdout)
	require.Len(t, lines, 1)

	var first mission.TraceSummary
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "m-2026-05-21-001", first.ID)
	assert.Equal(t, "mdm", first.Worker)
}

func TestFindMissions_StatusFilter(t *testing.T) {
	_, repo := findMissionsEnv(t)
	seedMissionsJSONL(t, repo, sampleTraces())

	stdout, _, err := execHandler(t, "find", "missions", "--status", "failed")
	require.NoError(t, err)

	lines := splitNonEmpty(stdout)
	require.Len(t, lines, 1)

	var first mission.TraceSummary
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "m-2026-05-21-001", first.ID)
	assert.Equal(t, "failed", first.Status)
}

func TestFindMissions_CombinedFilters(t *testing.T) {
	_, repo := findMissionsEnv(t)
	seedMissionsJSONL(t, repo, sampleTraces())

	// Worker=bwk AND since=2026-05-22 should yield only the third trace.
	stdout, _, err := execHandler(t, "find", "missions",
		"--worker", "bwk", "--since", "2026-05-22T00:00:00Z")
	require.NoError(t, err)

	lines := splitNonEmpty(stdout)
	require.Len(t, lines, 1)

	var first mission.TraceSummary
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "m-2026-05-22-001", first.ID)
}

func TestFindMissions_FormatTable(t *testing.T) {
	_, repo := findMissionsEnv(t)
	seedMissionsJSONL(t, repo, sampleTraces())

	stdout, _, err := execHandler(t, "find", "missions", "--format", "table")
	require.NoError(t, err)

	// The table output should contain column headers and data rows.
	assert.Contains(t, stdout, "ID")
	assert.Contains(t, stdout, "STATUS")
	assert.Contains(t, stdout, "WORKER")
	assert.Contains(t, stdout, "VERDICT")
	assert.Contains(t, stdout, "m-2026-05-20-001")
	assert.Contains(t, stdout, "bwk")
	assert.Contains(t, stdout, "pass")
}

func TestFindMissions_FormatPaths(t *testing.T) {
	_, repo := findMissionsEnv(t)
	seedMissionsJSONL(t, repo, sampleTraces())

	stdout, _, err := execHandler(t, "find", "missions", "--format", "paths")
	require.NoError(t, err)

	lines := splitNonEmpty(stdout)
	require.Len(t, lines, 3)
	for _, line := range lines {
		assert.True(t, strings.HasPrefix(line, repo),
			"path should start with repo root: %s", line)
		assert.Contains(t, line, ".punt-labs/ethos/missions/m-")
	}
}

func TestFindMissions_EmptyFile(t *testing.T) {
	_, repo := findMissionsEnv(t)
	seedMissionsJSONL(t, repo, nil)

	stdout, _, err := execHandler(t, "find", "missions")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(stdout))
}

func TestFindMissions_MissingFile(t *testing.T) {
	findMissionsEnv(t)
	// No missions.jsonl seeded — file does not exist.

	stdout, _, err := execHandler(t, "find", "missions")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(stdout))
}

func TestFindMissions_InvalidFormat(t *testing.T) {
	findMissionsEnv(t)

	_, stderr, err := execHandler(t, "find", "missions", "--format", "yaml")
	require.Error(t, err)
	_, isUsage := err.(usageError)
	assert.True(t, isUsage, "expected usageError, got %T: %v", err, err)
	assert.Contains(t, stderr, "--format must be json, table, or paths")
}

func TestFindMissions_OutsideRepo(t *testing.T) {
	nonRepo, err := os.MkdirTemp("/tmp", "ethos-find-norepo-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(nonRepo) })
	home, err := os.MkdirTemp("/tmp", "ethos-find-home-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(home) })

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(nonRepo)
	resetFindMissionsFlags()
	t.Cleanup(resetFindMissionsFlags)

	_, stderr, err := execHandler(t, "find", "missions")
	require.Error(t, err)
	_, isUsage := err.(usageError)
	assert.True(t, isUsage, "expected usageError, got %T: %v", err, err)
	assert.Contains(t, stderr, "must run inside a repo")
}
