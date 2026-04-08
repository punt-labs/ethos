package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ethosBinary is the path to the compiled ethos binary, built once
// per test run by TestMain. Empty if the build failed; subprocess
// tests skip in that case while in-process tests still run.
var ethosBinary string

// TestMain builds the ethos binary into a temp file before running
// any tests. Subprocess tests need this so they can exercise the
// runtime os.Exit error paths in `runMissionCreate` (the in-process
// captureStdout pattern would crash on os.Exit).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ethos-cmd-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir for binary: %v\n", err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "ethos")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "go build for cmd/ethos tests failed: %v\n", err)
		// Leave ethosBinary empty; subprocess tests will skip and the
		// other tests still run.
	} else {
		ethosBinary = bin
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// missionTestEnv sets HOME to a fresh temp directory, resets the
// global flag state used by mission commands, and seeds the
// canonical `djb` evaluator identity. Phase 3.3's frozen-evaluator
// invariant requires the contract's evaluator handle to resolve to
// real personality, writing-style, and talent content at create
// time; tests that exercise the CLI create path would otherwise
// fail with `evaluator not found` because the temp HOME starts
// empty. Returns the HOME root.
func missionTestEnv(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Seed the evaluator identity into the global store
	// (~/.punt-labs/ethos), which is what the CLI's identityStore()
	// resolves to when there is no repo-local .punt-labs/ethos. The
	// content is fixed placeholder text — every test that creates a
	// mission needs the same djb identity, so a single shared seed
	// keeps the fixture cost out of every test body.
	seedEvaluator(t, filepath.Join(tmp, ".punt-labs", "ethos"))

	// Reset the package-level flag globals so cross-test contamination
	// (e.g. a leaked --json from a prior test) does not bleed in.
	jsonOutput = false
	missionCreateFile = ""
	missionListStatus = "open"
	missionCloseStatus = mission.StatusClosed
	missionResultFile = ""
	t.Cleanup(func() {
		jsonOutput = false
		missionCreateFile = ""
		missionListStatus = "open"
		missionCloseStatus = mission.StatusClosed
		missionResultFile = ""
	})
	return tmp
}

// seedEvaluator drops a minimal djb identity (the canonical evaluator
// every contract names) into the given root. Mirrors the MCP test
// helper testHandlerWithMissions so the CLI tests no longer rely on
// a magically-pre-existing identity at the user's home directory.
func seedEvaluator(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(root, 0o700))

	require.NoError(t, attribute.NewStore(root, attribute.Personalities).Save(&attribute.Attribute{
		Slug:    "bernstein",
		Content: "# Bernstein\n\nFrozen-evaluator placeholder.\n",
	}))
	require.NoError(t, attribute.NewStore(root, attribute.WritingStyles).Save(&attribute.Attribute{
		Slug:    "bernstein-prose",
		Content: "# Bernstein Prose\n\nPlaceholder.\n",
	}))
	require.NoError(t, attribute.NewStore(root, attribute.Talents).Save(&attribute.Attribute{
		Slug:    "security",
		Content: "# Security\n",
	}))
	require.NoError(t, identity.NewStore(root).Save(&identity.Identity{
		Name:         "Dan B",
		Handle:       "djb",
		Kind:         "agent",
		Personality:  "bernstein",
		WritingStyle: "bernstein-prose",
		Talents:      []string{"security"},
	}))
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

// writeResultFile drops a minimal valid result YAML into a temp file
// and returns the path. The helper is parameterized by mission ID
// and round so tests that exercise the CLI result path can drive
// the Phase 3.6 close gate without re-typing the YAML at every call
// site.
func writeResultFile(t *testing.T, missionID string, round int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "result.yaml")
	body := fmt.Sprintf(`mission: %s
round: %d
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: make check
    status: pass
`, missionID, round)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// submitCLIResult runs the CLI result subcommand in-process so tests
// that only care about the Phase 3.6 gate's presence can stay brief.
// It always submits a pass/0.9/round-1 result — tests that need a
// different shape build the YAML and invoke runMissionResult
// directly.
func submitCLIResult(t *testing.T, missionID string, round int) {
	t.Helper()
	oldFile := missionResultFile
	missionResultFile = writeResultFile(t, missionID, round)
	t.Cleanup(func() { missionResultFile = oldFile })
	captureStdout(t, func() { runMissionResult(missionID, missionResultFile) })
}

// writeContractFileWithWriteSet writes a contract file with a custom
// write_set, returning the file path. Tests that create more than one
// mission in the same store must use disjoint write_sets to bypass
// the Phase 3.2 cross-mission conflict check.
func writeContractFileWithWriteSet(t *testing.T, writeSet ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "contract.yaml")
	var ws strings.Builder
	for _, w := range writeSet {
		ws.WriteString("  - ")
		ws.WriteString(w)
		ws.WriteString("\n")
	}
	body := `leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.5
write_set:
` + ws.String() + `success_criteria:
  - make check passes
budget:
  rounds: 3
  reflection_after_each: true
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
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
		// Clear cobra's root args slice so the next test that uses the
		// rootCmd directly doesn't see leftover args from this test.
		rootCmd.SetArgs(nil)
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
	// Tightened from "required flag" to the specific flag name so a
	// future regression that demands a different required flag would
	// not silently match.
	assert.Contains(t, stderr, `required flag(s) "file"`)
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

// TestMissionShow_HashOnOwnLine asserts the M1 fix: `mission show`
// renders the 64-character evaluator hash on its own "Hash:" row, not
// inline on the "Evaluator:" row. An inline hash produces a
// ~116-character line that wraps on 80-column terminals.
//
// The test uses a freshly created mission through the CLI path so the
// evaluator hash is whatever ApplyServerFields computed — the exact
// value does not matter; only that the row exists, carries a
// 64-character hex, and sits on its own physical line.
func TestMissionShow_HashOnOwnLine(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	out := captureStdout(t, func() { runMissionShow(id) })

	// The Evaluator row still names the handle and pinned timestamp,
	// but no longer carries the hash inline.
	assert.Contains(t, out, "Evaluator:")
	assert.Contains(t, out, "(pinned ")
	assert.NotContains(t, out, ", hash ",
		"hash must not be folded into the Evaluator line")

	// A dedicated Hash row follows. The hex value is 64 characters;
	// we confirm the row exists and the line it lives on contains a
	// 64-char hex substring.
	lines := strings.Split(out, "\n")
	var hashLine string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "Hash:") {
			hashLine = l
			break
		}
	}
	require.NotEmpty(t, hashLine, "Hash row must be present for a 3.3+ mission")
	// Count hex characters on the hash line. tabwriter may pad with
	// spaces, so we scan for a contiguous 64-char hex sequence.
	assert.Regexp(t, `[0-9a-f]{64}`, hashLine,
		"Hash row must contain a 64-char hex sha256")
}

// --- list ---

func TestMissionList_Empty(t *testing.T) {
	missionTestEnv(t)
	out := captureStdout(t, func() { runMissionList("open") })
	assert.Contains(t, out, "No missions found")
}

func TestMissionList_FilterByStatus(t *testing.T) {
	missionTestEnv(t)

	// Create three missions with disjoint write_sets so Phase 3.2's
	// cross-mission conflict check does not collapse them.
	missionCreateFile = writeContractFileWithWriteSet(t, "tests/list-a/")
	captureStdout(t, runMissionCreate)
	missionCreateFile = writeContractFileWithWriteSet(t, "tests/list-b/")
	captureStdout(t, runMissionCreate)
	missionCreateFile = writeContractFileWithWriteSet(t, "tests/list-c/")
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 3)

	// Close one. The other two stay open. Phase 3.6 requires a
	// result artifact for the current round before the close gate
	// will accept the terminal transition.
	submitCLIResult(t, ids[0], 1)
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

	// Phase 3.6: submit a result before closing so the gate is
	// satisfied. The test exercises the close path, not the gate
	// refusal — the refusal branch is covered separately.
	submitCLIResult(t, ids[0], 1)

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

	submitCLIResult(t, ids[0], 1)

	prefix := ids[0][:9]
	captureStdout(t, func() { runMissionClose(prefix, mission.StatusFailed) })

	c, err := ms.Load(ids[0])
	require.NoError(t, err)
	assert.Equal(t, mission.StatusFailed, c.Status)
}

// --- 3.4: reflect, reflections, advance ---

// writeReflectionFile drops a reflection YAML body into a temp file
// and returns the path. The body is parameterized by round and
// recommendation so the same helper covers continue, pivot, stop,
// and escalate cases.
func writeReflectionFile(t *testing.T, round int, recommendation, reason string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("reflection-%d.yaml", round))
	body := fmt.Sprintf(`round: %d
author: claude
converging: true
signals:
  - tests passing
  - lint clean
recommendation: %s
reason: %q
`, round, recommendation, reason)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestMissionReflect_RoundTrip(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	missionReflectFile = writeReflectionFile(t, 1, "continue", "round 1 went well")
	stdout := captureStdout(t, func() { runMissionReflect(ids[0], missionReflectFile) })
	assert.Empty(t, strings.TrimSpace(stdout), "reflect must be silent on success (non-JSON mode)")

	rs, err := ms.LoadReflections(ids[0])
	require.NoError(t, err)
	require.Len(t, rs, 1)
	assert.Equal(t, "continue", rs[0].Recommendation)
}

func TestMissionReflect_JSON(t *testing.T) {
	missionTestEnv(t)
	jsonOutput = true
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	missionReflectFile = writeReflectionFile(t, 1, "continue", "ok")
	out := captureStdout(t, func() { runMissionReflect(ids[0], missionReflectFile) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, ids[0], got["mission_id"])
	assert.Equal(t, float64(1), got["round"])
	assert.Equal(t, "continue", got["recommendation"])
	assert.NotEmpty(t, got["created_at"])
}

func TestMissionAdvance_RequiresReflection(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	home := t.TempDir()
	seedEvaluator(t, filepath.Join(home, ".punt-labs", "ethos"))
	tmp := t.TempDir()
	contract := filepath.Join(tmp, "contract.yaml")
	require.NoError(t, os.WriteFile(contract, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.8
write_set:
  - tests/advance-noreflect/
success_criteria:
  - make check passes
budget:
  rounds: 3
`), 0o600))

	env := append(os.Environ(), "HOME="+home)

	// Create the mission.
	cmd := exec.Command(ethosBinary, "mission", "create", "--file", contract)
	cmd.Env = env
	require.NoError(t, cmd.Run())

	// List to discover the ID.
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

	// Try to advance without a reflection — must exit 1 with the
	// "no reflection for round 1" message on stderr.
	advCmd := exec.Command(ethosBinary, "mission", "advance", id)
	advCmd.Env = env
	var advErr bytes.Buffer
	advCmd.Stderr = &advErr
	err := advCmd.Run()
	require.Error(t, err)
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok)
	assert.Equal(t, 1, exitErr.ExitCode())
	stderr := advErr.String()
	assert.Contains(t, stderr, "no reflection for round 1")
	assert.Contains(t, stderr, id)
}

func TestMissionAdvance_HappyPath(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	// Reflect on round 1.
	missionReflectFile = writeReflectionFile(t, 1, "continue", "ok")
	captureStdout(t, func() { runMissionReflect(id, missionReflectFile) })

	// Advance — non-JSON mode is silent on success (matches every
	// other mission subcommand: create, close, reflect). Exit code 0
	// and a bumped CurrentRound on disk tell the whole story.
	out := captureStdout(t, func() { runMissionAdvance(id) })
	assert.Empty(t, strings.TrimSpace(out),
		"mission advance must be silent on success in non-JSON mode")

	loaded, err := ms.Load(id)
	require.NoError(t, err)
	assert.Equal(t, 2, loaded.CurrentRound)
}

// TestMissionShow_RendersCurrentRound asserts that printContract
// includes the new "Round: N of M" line so the operator can read
// the mission's progress at a glance.
func TestMissionShow_RendersCurrentRound(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	out := captureStdout(t, func() { runMissionShow(ids[0]) })
	assert.Contains(t, out, "Round:")
	assert.Contains(t, out, "1 of 3")
}

// TestMissionReflections_JSON asserts that the reflections command
// returns an array (never null) and that each entry round-trips
// through the CLI's strict decoder.
func TestMissionReflections_JSON(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	id := ids[0]

	// Empty case — no reflections yet, must produce "[]" (not "null").
	jsonOutput = true
	out := captureStdout(t, func() { runMissionReflections(id) })
	assert.Equal(t, "[]", strings.TrimSpace(out))

	// Submit a reflection and re-fetch.
	missionReflectFile = writeReflectionFile(t, 1, "continue", "ok")
	captureStdout(t, func() { runMissionReflect(id, missionReflectFile) })

	out = captureStdout(t, func() { runMissionReflections(id) })
	var got []mission.Reflection
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 1)
	assert.Equal(t, 1, got[0].Round)
	assert.Equal(t, "continue", got[0].Recommendation)
}

// --- 3.6: result submission and close gate ---

// TestMissionResult_RoundTrip asserts success criterion 1 via the CLI
// surface: a well-formed result YAML persists through runMissionResult
// and comes back via LoadResult.
func TestMissionResult_RoundTrip(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	missionResultFile = writeResultFile(t, id, 1)
	stdout := captureStdout(t, func() { runMissionResult(id, missionResultFile) })
	assert.Empty(t, strings.TrimSpace(stdout), "result must be silent on success (non-JSON mode)")

	loaded, err := ms.LoadResult(id, 1)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, 1, loaded.Round)
	assert.Equal(t, mission.VerdictPass, loaded.Verdict)
}

// TestMissionResult_JSON asserts the JSON output shape for the CLI
// result subcommand.
func TestMissionResult_JSON(t *testing.T) {
	missionTestEnv(t)
	jsonOutput = true
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	id := ids[0]

	missionResultFile = writeResultFile(t, id, 1)
	out := captureStdout(t, func() { runMissionResult(id, missionResultFile) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, id, got["mission_id"])
	assert.Equal(t, float64(1), got["round"])
	assert.Equal(t, "pass", got["verdict"])
	assert.Equal(t, 0.9, got["confidence"])
	assert.NotEmpty(t, got["created_at"])
}

// TestMissionClose_GateRefusesWithoutResult asserts the CLI close
// path surfaces the Phase 3.6 gate refusal verbatim. Runs in a
// subprocess because runMissionClose calls os.Exit on gate refusal.
func TestMissionClose_GateRefusesWithoutResult(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	home := t.TempDir()
	seedEvaluator(t, filepath.Join(home, ".punt-labs", "ethos"))
	tmp := t.TempDir()
	contract := filepath.Join(tmp, "contract.yaml")
	require.NoError(t, os.WriteFile(contract, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.10
write_set:
  - tests/close-gate/
success_criteria:
  - make check passes
budget:
  rounds: 3
`), 0o600))

	env := append(os.Environ(), "HOME="+home)

	// Create the mission.
	cmd := exec.Command(ethosBinary, "mission", "create", "--file", contract)
	cmd.Env = env
	require.NoError(t, cmd.Run())

	// List to discover the ID.
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

	// Try to close without a result — must exit 1 with the gate
	// refusal message on stderr.
	closeCmd := exec.Command(ethosBinary, "mission", "close", id)
	closeCmd.Env = env
	var closeErr bytes.Buffer
	closeCmd.Stderr = &closeErr
	err := closeCmd.Run()
	require.Error(t, err)
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok)
	assert.Equal(t, 1, exitErr.ExitCode())
	stderr := closeErr.String()
	assert.Contains(t, stderr, id)
	assert.Contains(t, stderr, "no result artifact for round 1")
	assert.Contains(t, stderr, "ethos mission result")
}

// TestMissionClose_GateAcceptsWithResult is the positive counterpart
// to TestMissionClose_GateRefusesWithoutResult: submitting a result
// via the CLI result subcommand and then closing via the CLI close
// subcommand succeeds.
func TestMissionClose_GateAcceptsWithResult(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	id := ids[0]

	submitCLIResult(t, id, 1)
	captureStdout(t, func() { runMissionClose(id, mission.StatusClosed) })

	loaded, err := ms.Load(id)
	require.NoError(t, err)
	assert.Equal(t, mission.StatusClosed, loaded.Status)
	assert.NotEmpty(t, loaded.ClosedAt)
}

// TestMissionResult_AppendOnlyViaCLI asserts success criterion 3
// through the CLI surface: a duplicate submission for the same
// round fails. Runs in a subprocess because the second invocation
// calls os.Exit on the append-only refusal.
func TestMissionResult_AppendOnlyViaCLI(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	home := t.TempDir()
	seedEvaluator(t, filepath.Join(home, ".punt-labs", "ethos"))
	tmp := t.TempDir()
	contract := filepath.Join(tmp, "contract.yaml")
	require.NoError(t, os.WriteFile(contract, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.10
write_set:
  - tests/append-only-cli/
success_criteria:
  - make check passes
budget:
  rounds: 3
`), 0o600))

	env := append(os.Environ(), "HOME="+home)
	createCmd := exec.Command(ethosBinary, "mission", "create", "--file", contract)
	createCmd.Env = env
	require.NoError(t, createCmd.Run())

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

	resultBody := fmt.Sprintf(`mission: %s
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: make check
    status: pass
`, id)
	resultFile := filepath.Join(tmp, "result.yaml")
	require.NoError(t, os.WriteFile(resultFile, []byte(resultBody), 0o600))

	first := exec.Command(ethosBinary, "mission", "result", id, "--file", resultFile)
	first.Env = env
	require.NoError(t, first.Run())

	second := exec.Command(ethosBinary, "mission", "result", id, "--file", resultFile)
	second.Env = env
	var secondErr bytes.Buffer
	second.Stderr = &secondErr
	err := second.Run()
	require.Error(t, err)
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok)
	assert.Equal(t, 1, exitErr.ExitCode())
	stderr := secondErr.String()
	assert.Contains(t, stderr, "append-only")
	assert.Contains(t, stderr, "round 1")
	assert.Contains(t, stderr, id)
}

// TestMissionCreate_ConflictRejectedSubprocess exercises the Phase 3.2
// admission control through the real CLI binary. The first invocation
// creates a mission with write_set [internal/foo/]; the second
// invocation tries to create an overlapping mission with write_set
// [internal/foo/bar.go] and must fail with exit code 1, with stderr
// naming the existing mission and the overlapping path.
//
// The test runs in a subprocess because runMissionCreate calls
// os.Exit on error — an in-process test would crash the test runner
// when the conflict path fires.
func TestMissionCreate_ConflictRejectedSubprocess(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	home := t.TempDir()
	// Phase 3.3 requires the evaluator handle to resolve at create
	// time, so seed the canonical djb identity into the subprocess
	// HOME exactly the way the in-process tests do via missionTestEnv.
	seedEvaluator(t, filepath.Join(home, ".punt-labs", "ethos"))
	tmp := t.TempDir()

	contractA := filepath.Join(tmp, "a.yaml")
	require.NoError(t, os.WriteFile(contractA, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.6
write_set:
  - internal/foo/
success_criteria:
  - make check passes
budget:
  rounds: 3
`), 0o600))

	contractB := filepath.Join(tmp, "b.yaml")
	require.NoError(t, os.WriteFile(contractB, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.6
write_set:
  - internal/foo/bar.go
success_criteria:
  - make check passes
budget:
  rounds: 3
`), 0o600))

	env := append(os.Environ(), "HOME="+home)

	// First create — must succeed.
	cmd := exec.Command(ethosBinary, "mission", "create", "--file", contractA)
	cmd.Env = env
	var outA, errA bytes.Buffer
	cmd.Stdout = &outA
	cmd.Stderr = &errA
	require.NoError(t, cmd.Run(), "first create failed: %s", errA.String())

	// Find the created mission ID via List so the conflict assertion
	// can check stderr names it.
	listCmd := exec.Command(ethosBinary, "mission", "list", "--json")
	listCmd.Env = env
	var listOut bytes.Buffer
	listCmd.Stdout = &listOut
	listCmd.Stderr = os.Stderr
	require.NoError(t, listCmd.Run())
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(listOut.Bytes(), &entries))
	require.Len(t, entries, 1)
	createdID, _ := entries[0]["mission_id"].(string)
	require.NotEmpty(t, createdID)

	// Second create — must fail with exit code 1.
	cmd = exec.Command(ethosBinary, "mission", "create", "--file", contractB)
	cmd.Env = env
	var outB, errB bytes.Buffer
	cmd.Stdout = &outB
	cmd.Stderr = &errB
	err := cmd.Run()
	require.Error(t, err, "overlapping create must fail")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected ExitError, got %T: %v", err, err)
	assert.Equal(t, 1, exitErr.ExitCode(), "exit code must be 1")

	stderr := errB.String()
	assert.Contains(t, stderr, "ethos: mission create:")
	assert.Contains(t, stderr, "write_set conflict with mission")
	assert.Contains(t, stderr, createdID)
	assert.Contains(t, stderr, "worker: bwk")
	assert.Contains(t, stderr, "internal/foo/bar.go")
}

// seedRoleOverlapFixture writes a fully-populated identity fixture
// under HOME/.punt-labs/ethos that ties two agents to the same team
// and the same role, so the Phase 3.5 role-overlap gate has something
// to refuse when the CLI create path wires its RoleLister via
// NewLiveHashSources.
//
// Deliberately writes YAML by hand instead of going through the
// package CRUD APIs — the test is exercising the CLI's live-store
// wiring, and the fixture layout must match what the layered stores
// read at runtime.
//
// The shared role is `go-specialist`; the shared team is `engineering`.
// Both agents (`bwk` and `mdm`) are members of engineering bound to
// go-specialist. Both agents also have the full personality/writing
// style/talent content the frozen-evaluator hash needs — otherwise
// the MCP and CLI create paths would fail on the hash step before
// ever reaching the overlap gate.
func seedRoleOverlapFixture(t *testing.T, home string) {
	t.Helper()
	root := filepath.Join(home, ".punt-labs", "ethos")
	// Start from the same base the happy-path create tests use.
	seedEvaluator(t, root)

	// Add personality, writing style, and talent for the second agent
	// so its identity resolves cleanly. Content is deliberately
	// different from djb's so a future hash assertion could
	// distinguish them, though this test only cares that both
	// identities load without warnings.
	require.NoError(t, attribute.NewStore(root, attribute.Personalities).Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nMethodical systems programmer.\n",
	}))
	require.NoError(t, attribute.NewStore(root, attribute.WritingStyles).Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.\n",
	}))
	require.NoError(t, attribute.NewStore(root, attribute.Talents).Save(&attribute.Attribute{
		Slug:    "go-systems",
		Content: "# Go Systems\n",
	}))
	require.NoError(t, identity.NewStore(root).Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
		Talents:      []string{"go-systems"},
	}))

	// Write the role file directly. Roles live under root/roles/<slug>.yaml;
	// a bare Save on the global store is the simplest path. The role
	// content does not matter for the overlap gate — only the name does —
	// but a minimal set of responsibilities keeps role.Validate happy.
	rolesDir := filepath.Join(root, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(rolesDir, "go-specialist.yaml"),
		[]byte("name: go-specialist\nresponsibilities:\n  - Go implementation\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(rolesDir, "security-reviewer.yaml"),
		[]byte("name: security-reviewer\nresponsibilities:\n  - Security review\n"),
		0o600,
	))

	// Write the team file directly. Both bwk and mdm start bound to
	// go-specialist; the second phase of the test rebinds djb to
	// security-reviewer so the overlap check passes.
	//
	// Note: seedEvaluator already created djb with its own attribute
	// content; we just add it as a team member so the live store
	// walking picks it up.
	teamsDir := filepath.Join(root, "teams")
	require.NoError(t, os.MkdirAll(teamsDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(teamsDir, "engineering.yaml"),
		[]byte(`name: engineering
members:
  - identity: bwk
    role: go-specialist
  - identity: djb
    role: go-specialist
`),
		0o600,
	))
}

// rewriteTeamWithDistinctRoles mutates the fixture so djb is bound
// to security-reviewer instead of go-specialist. Used between the
// two halves of TestMissionCreate_RoleOverlapThroughLiveStoresSubprocess
// to show the recovery path is exactly "rebind one side to a distinct
// role and try again."
func rewriteTeamWithDistinctRoles(t *testing.T, home string) {
	t.Helper()
	teamFile := filepath.Join(home, ".punt-labs", "ethos", "teams", "engineering.yaml")
	require.NoError(t, os.WriteFile(teamFile, []byte(`name: engineering
members:
  - identity: bwk
    role: go-specialist
  - identity: djb
    role: security-reviewer
`), 0o600))
}

// TestMissionCreate_RoleOverlapThroughLiveStoresSubprocess exercises
// the Phase 3.5 role-overlap gate through the real CLI wiring: from
// `ethos mission create` down through identityStore → layeredRoleStore
// → layeredTeamStore → NewLiveHashSources → WithRoleLister → Store.Create.
// A unit test that fakes the RoleLister cannot catch a wiring bug in
// that chain; this subprocess test is the only gate.
//
// Two spawns in one test:
//  1. worker=bwk, evaluator=djb, both bound to engineering/go-specialist.
//     Must exit 1 with the role-overlap error naming both handles, the
//     shared binding, and the recovery hint.
//  2. After rewriting the team file so djb is bound to
//     engineering/security-reviewer, the same contract must succeed —
//     the "rebind one side to a distinct role" recovery path is
//     executable in-process.
func TestMissionCreate_RoleOverlapThroughLiveStoresSubprocess(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	home := t.TempDir()
	seedRoleOverlapFixture(t, home)

	// The ethos repo's own .punt-labs/ethos/ submodule already defines
	// bwk and djb with DISTINCT roles (engineering/go-specialist and
	// engineering/security-engineer). Running the binary inside the
	// repo would pick up the repo-local identity store via
	// FindRepoEthosRoot, overriding the test fixture and defeating the
	// overlap assertion. Give the child a CWD with its own bare .git
	// so FindRepoRoot stops outside the ethos repo.
	repo := t.TempDir()
	gitInit := exec.Command("git", "init", repo)
	gitInit.Env = append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := gitInit.CombinedOutput()
	require.NoError(t, err, "git init failed: %s", out)

	contract := filepath.Join(repo, "contract.yaml")
	require.NoError(t, os.WriteFile(contract, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.9
write_set:
  - internal/mission/
success_criteria:
  - make check passes
budget:
  rounds: 3
`), 0o600))

	env := append(os.Environ(), "HOME="+home)

	// Phase 1: both agents share engineering/go-specialist; create
	// must fail with the overlap error.
	cmd := exec.Command(ethosBinary, "mission", "create", "--file", contract)
	cmd.Env = env
	cmd.Dir = repo
	var outA, errA bytes.Buffer
	cmd.Stdout = &outA
	cmd.Stderr = &errA
	err = cmd.Run()
	require.Error(t, err, "role-overlapping create must fail")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected ExitError, got %T: %v", err, err)
	assert.Equal(t, 1, exitErr.ExitCode(), "exit code must be 1")

	stderr := errA.String()
	assert.Contains(t, stderr, "ethos: mission create:")
	assert.Contains(t, stderr, "bwk")
	assert.Contains(t, stderr, "djb")
	assert.Contains(t, stderr, "engineering/go-specialist")
	assert.Contains(t, stderr, "recovery")

	// Phase 2: rebind djb to a distinct role; the same contract must
	// now succeed. This is the recovery path the error message
	// surfaces, executed verbatim.
	rewriteTeamWithDistinctRoles(t, home)

	cmd = exec.Command(ethosBinary, "mission", "create", "--file", contract)
	cmd.Env = env
	cmd.Dir = repo
	var outB, errB bytes.Buffer
	cmd.Stdout = &outB
	cmd.Stderr = &errB
	require.NoError(t, cmd.Run(),
		"after rebinding djb to security-reviewer, create must succeed: %s",
		errB.String())
}
