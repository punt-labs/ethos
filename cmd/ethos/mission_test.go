package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// missionTestEnv sets HOME to a fresh temp directory and resets the
// global flag state used by mission commands. The returned path is the
// HOME root for the test.
func missionTestEnv(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Reset the package-level flag globals so cross-test contamination
	// (e.g. a leaked --json from a prior test) does not bleed in.
	jsonOutput = false
	missionCreateFile = ""
	missionListStatus = "open"
	missionCloseStatus = mission.StatusClosed
	t.Cleanup(func() {
		jsonOutput = false
		missionCreateFile = ""
		missionListStatus = "open"
		missionCloseStatus = mission.StatusClosed
	})
	return tmp
}

// writeContractFile drops a YAML contract into a temp file and returns
// the path. The contract omits server-controlled fields — mission_id,
// status, created_at, updated_at, and evaluator.pinned_at — because the
// CLI fills them in from time.Now() and the ID generator. That makes
// this helper valid only for CLI-path tests, not for direct
// store.Create calls (those need to set pinned_at explicitly).
func writeContractFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "contract.yaml")
	body := `leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.5
  files:
    - internal/session/store.go
write_set:
  - internal/mission/
  - cmd/ethos/mission.go
tools:
  - Read
  - Write
success_criteria:
  - make check passes
budget:
  rounds: 3
  reflection_after_each: true
context: "smoke test contract"
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// captureStderr runs fn while capturing os.Stderr and returns the output.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()

	w.Close()
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

// runCobra runs a command through the rootCmd Execute path with the
// given args, capturing both stdout and stderr. Used for tests that
// exercise cobra's flag parsing and subcommand dispatch.
func runCobra(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	oldOut := rootCmd.OutOrStdout()
	oldErr := rootCmd.ErrOrStderr()
	var outBuf, errBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	defer func() {
		rootCmd.SetOut(oldOut)
		rootCmd.SetErr(oldErr)
	}()

	rootCmd.SetArgs(args)
	err = rootCmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// --- create ---

func TestMissionCreate_FromFile(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)

	// Non-JSON mode is silent on success.
	stdout := captureStdout(t, runMissionCreate)
	assert.Empty(t, strings.TrimSpace(stdout), "create must be silent on success (non-JSON mode)")

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	c, err := ms.Load(ids[0])
	require.NoError(t, err)
	assert.Equal(t, mission.StatusOpen, c.Status)
	assert.Equal(t, "claude", c.Leader)
	assert.Equal(t, "bwk", c.Worker)
	assert.Equal(t, "djb", c.Evaluator.Handle)
	assert.NotEmpty(t, c.CreatedAt)
	assert.NotEmpty(t, c.Evaluator.PinnedAt)
}

func TestMissionCreate_FromFileJSON(t *testing.T) {
	missionTestEnv(t)
	jsonOutput = true

	missionCreateFile = writeContractFile(t)
	out := captureStdout(t, runMissionCreate)

	var c mission.Contract
	require.NoError(t, json.Unmarshal([]byte(out), &c))
	assert.Equal(t, mission.StatusOpen, c.Status)
	assert.NotEmpty(t, c.MissionID)
}

func TestMissionCreate_RequiresFile(t *testing.T) {
	missionTestEnv(t)

	_, stderr, err := runCobra(t, "mission", "create")
	require.Error(t, err, "create without --file must fail")
	assert.Contains(t, stderr, "required flag")
}

// --- bare mission command ---

func TestMissionBareShowsHelp(t *testing.T) {
	missionTestEnv(t)

	// Bare `ethos mission` must show help (cobra's default behavior
	// when a command has subcommands and no Run).
	stdout, _, err := runCobra(t, "mission")
	require.NoError(t, err)
	// Cobra help lists Available Commands.
	assert.Contains(t, stdout, "Available Commands")
	assert.Contains(t, stdout, "create")
	assert.Contains(t, stdout, "show")
	assert.Contains(t, stdout, "list")
	assert.Contains(t, stdout, "close")
}

// --- show ---

func TestMissionShow(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	out := captureStdout(t, func() { runMissionShow(id) })
	assert.Contains(t, out, id)
	assert.Contains(t, out, "claude")
	assert.Contains(t, out, "bwk")
	assert.Contains(t, out, "djb")
	assert.Contains(t, out, "internal/mission/")
	// Tools must be rendered as a bullet list, not Go slice syntax.
	assert.Contains(t, out, "- Read")
	assert.Contains(t, out, "- Write")
	assert.NotContains(t, out, "[Read Write]")
}

func TestMissionShow_Prefix(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	// First 8 characters: "m-2026-0" — enough to disambiguate a single
	// mission in a fresh store.
	prefix := ids[0][:8]
	out := captureStdout(t, func() { runMissionShow(prefix) })
	assert.Contains(t, out, ids[0])
}

func TestMissionShow_JSON(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	jsonOutput = true
	out := captureStdout(t, func() { runMissionShow(ids[0]) })

	var c mission.Contract
	require.NoError(t, json.Unmarshal([]byte(out), &c))
	assert.Equal(t, ids[0], c.MissionID)
}

// --- list ---

func TestMissionList_Empty(t *testing.T) {
	missionTestEnv(t)
	out := captureStdout(t, func() { runMissionList("open") })
	assert.Contains(t, out, "No missions found")
}

func TestMissionList_FilterByStatus(t *testing.T) {
	missionTestEnv(t)

	// Create three missions.
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)
	captureStdout(t, runMissionCreate)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 3)

	// Close one. The other two stay open.
	require.NoError(t, ms.Close(ids[0], mission.StatusClosed))

	// Default filter "open" returns the two open ones.
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() { runMissionList("open") })
	var openEntries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &openEntries))
	assert.Len(t, openEntries, 2)

	// "all" returns all three.
	out = captureStdout(t, func() { runMissionList("all") })
	var allEntries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &allEntries))
	assert.Len(t, allEntries, 3)

	// "closed" returns just the one.
	out = captureStdout(t, func() { runMissionList("closed") })
	var closedEntries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &closedEntries))
	assert.Len(t, closedEntries, 1)
}

// --- close ---

func TestMissionClose(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	// Non-JSON mode is silent on success.
	stdout := captureStdout(t, func() { runMissionClose(ids[0], mission.StatusClosed) })
	assert.Empty(t, strings.TrimSpace(stdout), "close must be silent on success (non-JSON mode)")

	c, err := ms.Load(ids[0])
	require.NoError(t, err)
	assert.Equal(t, mission.StatusClosed, c.Status)
	assert.NotEmpty(t, c.ClosedAt)
}

func TestMissionClose_PrefixMatch(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	prefix := ids[0][:9]
	captureStdout(t, func() { runMissionClose(prefix, mission.StatusFailed) })

	c, err := ms.Load(ids[0])
	require.NoError(t, err)
	assert.Equal(t, mission.StatusFailed, c.Status)
}
