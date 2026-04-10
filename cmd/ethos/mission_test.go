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
	missionResultVerify = false
	missionResultBase = "main"
	t.Cleanup(func() {
		jsonOutput = false
		missionCreateFile = ""
		missionListStatus = "open"
		missionCloseStatus = mission.StatusClosed
		missionResultFile = ""
		missionResultVerify = false
		missionResultBase = "main"
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

	// Text mode echoes a one-line `created: <id> worker=... evaluator=...`
	// summary so a scripting caller can chain on the new mission ID
	// without a follow-up `ethos mission list` (ethos-30c).
	stdout := captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	assert.Contains(t, stdout, "created:")
	assert.Contains(t, stdout, ids[0])
	assert.Contains(t, stdout, "worker=bwk")
	assert.Contains(t, stdout, "evaluator=djb")

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
	_, err = ms.Close(ids[0], mission.StatusClosed)
	require.NoError(t, err)

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

	// Text mode echoes a one-line summary including the round and
	// verdict that authorized the close, so a scripting caller sees
	// the operation landed and which result satisfied the gate
	// without a follow-up show or mission log (ethos-30c).
	stdout := captureStdout(t, func() { runMissionClose(ids[0], mission.StatusClosed) })
	assert.Contains(t, stdout, "closed:")
	assert.Contains(t, stdout, ids[0])
	assert.Contains(t, stdout, "round=1")
	assert.Contains(t, stdout, "verdict=pass")
	assert.Contains(t, stdout, "status="+mission.StatusClosed)

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
	// Text mode echoes `reflected: <id> round=1 rec=continue` so a
	// scripting caller sees the reflection landed (ethos-30c).
	assert.Contains(t, stdout, "reflected:")
	assert.Contains(t, stdout, ids[0])
	assert.Contains(t, stdout, "round=1")
	assert.Contains(t, stdout, "rec=continue")

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

	// Advance — text mode echoes `advanced: <id> round 1 -> 2` so a
	// scripting caller can read the new round without a follow-up
	// show (ethos-30c).
	out := captureStdout(t, func() { runMissionAdvance(id) })
	assert.Contains(t, out, "advanced:")
	assert.Contains(t, out, id)
	assert.Contains(t, out, "round 1 -> 2")

	loaded, err := ms.Load(id)
	require.NoError(t, err)
	assert.Equal(t, 2, loaded.CurrentRound)
}

// TestMissionShow_IncludesResults asserts the H2 fix: `mission
// show` surfaces the round-by-round result log under the contract
// header so an operator can see the verdict that authorized a
// close without `cat`-ing the sibling YAML file.
//
// Round 1 of Phase 3.6 rendered only the contract and reflections.
// mdm flagged the gap: after a valid submit + close, `ethos mission
// show` said nothing about the result. Round 2 added printResults
// to runMissionShow and a top-level `results` field to the JSON
// payload.
func TestMissionShow_IncludesResults(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	// Submit a valid result and close the mission so show renders
	// both the contract and the result.
	submitCLIResult(t, id, 1)
	captureStdout(t, func() { runMissionClose(id, mission.StatusClosed) })

	// Human mode: the Results section must appear with the round
	// and verdict.
	out := captureStdout(t, func() { runMissionShow(id) })
	assert.Contains(t, out, "Results:")
	assert.Contains(t, out, "round 1")
	assert.Contains(t, out, "pass")
	assert.Contains(t, out, "bwk")

	// JSON mode: the payload must carry a `results` array with one
	// entry whose verdict is "pass".
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	jsonOut := captureStdout(t, func() { runMissionShow(id) })
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &payload))
	results, ok := payload["results"].([]any)
	require.True(t, ok, "results must be a top-level array, got: %v", payload["results"])
	require.Len(t, results, 1)
	first, _ := results[0].(map[string]any)
	assert.Equal(t, "pass", first["verdict"])
	assert.Equal(t, float64(1), first["round"])
}

// TestMissionShow_EmptyResultsIsArray asserts the A2 round-3 fix:
// `mission show --json` on a mission with no submitted result must
// return `"results": []`, not `"results": null`. The round-2 guard
// — `if payload["results"] == nil { ... }` — was dead code because
// a typed-nil []mission.Result boxed into map[string]any produces
// an `any` whose *type* is non-nil even though its value is nil.
// Round 3 pre-initializes the slice before constructing the
// payload so JSON-encodes an empty array.
func TestMissionShow_EmptyResultsIsArray(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	jsonOut := captureStdout(t, func() { runMissionShow(id) })

	// Raw text: `"results": []` must appear, `"results": null` must
	// not. Both forms use the printJSON indent width so the exact
	// substring is stable across runs.
	assert.Contains(t, jsonOut, `"results": []`,
		"empty results must serialize as [], got: %s", jsonOut)
	assert.NotContains(t, jsonOut, `"results": null`,
		"empty results must not serialize as null, got: %s", jsonOut)

	// Parsed: results must be an []any{} (non-nil, empty), not nil.
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &payload))
	results, ok := payload["results"].([]any)
	require.True(t, ok, "results must be an array, got: %v", payload["results"])
	assert.Equal(t, 0, len(results))
}

// TestMissionShow_JSONIncludesSessionAndRepo asserts the C1 round-3
// fix: `mission show --json` round-trips the `session` and `repo`
// Contract fields when the source contract sets them. Round 2's
// hand-rolled payload map dropped both fields silently, causing
// load-bearing cross-session identity data to vanish from the MCP
// and CLI surfaces. Round 3 replaced the map with a ShowPayload
// struct that embeds *Contract, so every Contract field — including
// any added in the future — round-trips automatically.
func TestMissionShow_JSONIncludesSessionAndRepo(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	// Mutate the persisted contract in place to set session and
	// repo. The CLI create path does not accept these fields from
	// the YAML today, but the store's Update path does — the
	// round-trip test exercises what the show path sees, not the
	// create path.
	c, err := ms.Load(id)
	require.NoError(t, err)
	c.Session = "test-session-abc"
	c.Repo = "punt-labs/ethos"
	require.NoError(t, ms.Update(c))

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	jsonOut := captureStdout(t, func() { runMissionShow(id) })

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &payload))
	assert.Equal(t, "test-session-abc", payload["session"],
		"session must round-trip through show JSON, got: %v", payload["session"])
	assert.Equal(t, "punt-labs/ethos", payload["repo"],
		"repo must round-trip through show JSON, got: %v", payload["repo"])
}

// TestMissionShow_JSONOmitsEmptyOptionalFields asserts the C1
// round-3 fix preserves Contract json-tag `omitempty` semantics.
// An open mission with no context, no tools, and no closed_at
// must NOT emit those fields in JSON — the round-2 hand-rolled
// map unconditionally emitted every field, leaking
// "closed_at": "" and "context": "" into payloads for open
// missions and muddying the distinction between "absent" and
// "empty".
func TestMissionShow_JSONOmitsEmptyOptionalFields(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	// Clear context and tools so the omitempty fields are truly
	// empty. The fixture in writeContractFile sets context and
	// tools; this test needs the opposite shape.
	c, err := ms.Load(id)
	require.NoError(t, err)
	c.Context = ""
	c.Tools = nil
	require.NoError(t, ms.Update(c))

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	jsonOut := captureStdout(t, func() { runMissionShow(id) })

	// An open mission never has closed_at; it must not appear in
	// the payload.
	assert.NotContains(t, jsonOut, `"closed_at"`,
		"open mission must not emit closed_at (omitempty), got: %s", jsonOut)
	// Empty context must not appear.
	assert.NotContains(t, jsonOut, `"context"`,
		"empty context must be omitted (omitempty), got: %s", jsonOut)
	// Empty tools must not appear.
	assert.NotContains(t, jsonOut, `"tools"`,
		"empty tools must be omitted (omitempty), got: %s", jsonOut)
}

// TestMissionShow_JSONSurfacesCorruptResultsAsWarnings asserts the
// D1 round-3 fix: when LoadResults returns an error (corrupt
// sibling file on disk), `mission show --json` emits a top-level
// `warnings` array with the load failure instead of silently
// returning `"results": []`. Without this, a corrupt file was
// indistinguishable from "no result submitted".
func TestMissionShow_JSONSurfacesCorruptResultsAsWarnings(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	// Corrupt the sibling results file. The file path mirrors the
	// store's resultsPath layout — <root>/missions/<id>.results.yaml.
	resultsFile := filepath.Join(ms.Root(), "missions", id+".results.yaml")
	require.NoError(t, os.WriteFile(resultsFile, []byte("this: is: not: valid: yaml: {[\n"), 0o600))

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	jsonOut := captureStdout(t, func() { runMissionShow(id) })

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &payload))

	// results must still be present (an empty array) so the
	// payload remains parseable.
	results, ok := payload["results"].([]any)
	require.True(t, ok, "results must still be an array on load failure, got: %v", payload["results"])
	assert.Equal(t, 0, len(results))

	// warnings must carry the load failure.
	warnings, ok := payload["warnings"].([]any)
	require.True(t, ok, "warnings must be a top-level array, got: %v", payload["warnings"])
	require.NotEmpty(t, warnings)
	firstWarning, _ := warnings[0].(string)
	assert.Contains(t, firstWarning, "loading results",
		"warning must name the load failure, got: %s", firstWarning)
}

// TestMissionShow_PrintsEmptyResultsSection asserts the E1 round-3
// fix: `mission show` on a mission with no submitted result
// renders a "Results: (none)" block instead of printing nothing.
// Round 2's printResults returned early on an empty slice; an
// operator running show on a fresh mission saw no indication
// results were even expected.
func TestMissionShow_PrintsEmptyResultsSection(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	out := captureStdout(t, func() { runMissionShow(id) })
	assert.Contains(t, out, "Results:",
		"empty-results show must print the Results: header, got: %s", out)
	assert.Contains(t, out, "(none)",
		"empty-results show must print (none) marker, got: %s", out)
}

// TestMissionShow_CorruptResultsStillPrintsSection asserts the N1
// round-4 fix: when LoadResults fails on a corrupt `.results.yaml`
// sibling, `mission show` still renders the Results header and
// `(none)` marker on stdout, and the load failure surfaces on
// stderr. Without this, an operator piping `ethos mission show <id>
// 2>/dev/null | less` would see the contract block but nothing where
// the Results section should be, and the warning signal would be
// lost. Runs in a subprocess so stdout and stderr can be asserted
// independently — the in-process captureStdout helper only captures
// stdout, and the symmetry bug is precisely about what each stream
// carries.
func TestMissionShow_CorruptResultsStillPrintsSection(t *testing.T) {
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
  - tests/corrupt-results/
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

	// Corrupt the sibling results file. The file path mirrors the
	// store's resultsPath layout — <root>/missions/<id>.results.yaml.
	resultsFile := filepath.Join(home, ".punt-labs", "ethos", "missions", id+".results.yaml")
	require.NoError(t, os.WriteFile(resultsFile, []byte("this: is: not: valid: yaml: {[\n"), 0o600))

	showCmd := exec.Command(ethosBinary, "mission", "show", id)
	showCmd.Env = env
	var stdoutBuf, stderrBuf bytes.Buffer
	showCmd.Stdout = &stdoutBuf
	showCmd.Stderr = &stderrBuf
	require.NoError(t, showCmd.Run(),
		"mission show must exit 0 on corrupt results, got stderr: %s", stderrBuf.String())

	stdout := stdoutBuf.String()
	assert.Contains(t, stdout, "Results:",
		"corrupt-results show must print the Results: header on stdout, got: %s", stdout)
	assert.Contains(t, stdout, "(none)",
		"corrupt-results show must print (none) marker on stdout, got: %s", stdout)

	stderr := stderrBuf.String()
	assert.Contains(t, stderr, "loading results",
		"corrupt-results show must carry the load failure on stderr, got: %s", stderr)
}

// TestMissionShow_CorruptReflectionsStillPrintsSection asserts the
// round-6 Bugbot fix: when LoadReflections fails on a corrupt
// `.reflections.yaml` sibling, `mission show` still renders the
// Reflections header and `(none)` marker on stdout, and the load
// failure surfaces on stderr. Round 4 fixed the same class for
// Results (mdm N1); this test closes the parallel miss. Without
// this, an operator piping `ethos mission show <id> 2>/dev/null |
// less` would see the contract block but nothing where the
// Reflections section should be, and the warning signal would be
// lost. Runs in a subprocess so stdout and stderr can be asserted
// independently — the symmetry bug is precisely about what each
// stream carries.
func TestMissionShow_CorruptReflectionsStillPrintsSection(t *testing.T) {
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
  - tests/corrupt-reflections/
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

	// Corrupt the sibling reflections file. The file path mirrors
	// the store's reflectionsPath layout —
	// <root>/missions/<id>.reflections.yaml.
	reflectionsFile := filepath.Join(home, ".punt-labs", "ethos", "missions", id+".reflections.yaml")
	require.NoError(t, os.WriteFile(reflectionsFile, []byte("this: is: not: valid: yaml: {[\n"), 0o600))

	showCmd := exec.Command(ethosBinary, "mission", "show", id)
	showCmd.Env = env
	var stdoutBuf, stderrBuf bytes.Buffer
	showCmd.Stdout = &stdoutBuf
	showCmd.Stderr = &stderrBuf
	require.NoError(t, showCmd.Run(),
		"mission show must exit 0 on corrupt reflections, got stderr: %s", stderrBuf.String())

	stdout := stdoutBuf.String()
	assert.Contains(t, stdout, "Reflections:",
		"corrupt-reflections show must print the Reflections: header on stdout, got: %s", stdout)
	assert.Contains(t, stdout, "(none)",
		"corrupt-reflections show must print (none) marker on stdout, got: %s", stdout)

	stderr := stderrBuf.String()
	assert.Contains(t, stderr, "loading reflections",
		"corrupt-reflections show must carry the load failure on stderr, got: %s", stderr)
}

// TestMissionClose_HelpMentionsResultGate asserts the G1 round-3
// fix: `mission close --help` documents that a result artifact is
// required for the current round, mirroring `mission advance --help`.
// Without this paragraph, an operator reading only the close help
// is surprised by the "no result artifact for round N" refusal.
func TestMissionClose_HelpMentionsResultGate(t *testing.T) {
	missionTestEnv(t)
	stdout, _, err := runCobra(t, "mission", "close", "--help")
	require.NoError(t, err)
	// The gate language must surface the prerequisite and the
	// remediation path. Both pieces matter: the operator needs to
	// know what is required AND how to satisfy it.
	assert.Contains(t, stdout, "result",
		"close help must mention the result prerequisite, got: %s", stdout)
	assert.Contains(t, stdout, "ethos mission result",
		"close help must link to the result subcommand, got: %s", stdout)
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
	// Text mode echoes `result: <id> round=1 verdict=pass` so a
	// scripting caller can confirm the submission landed without
	// a follow-up `ethos mission results` (ethos-30c).
	assert.Contains(t, stdout, "result:")
	assert.Contains(t, stdout, id)
	assert.Contains(t, stdout, "round=1")
	assert.Contains(t, stdout, "verdict="+mission.VerdictPass)

	loaded, err := ms.LoadResult(id, 1)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, 1, loaded.Round)
	assert.Equal(t, mission.VerdictPass, loaded.Verdict)
}

// TestMissionResults_ListsSubmittedResults asserts the H3 fix:
// `ethos mission results <id>` is a real subcommand, and it lists
// the round-by-round worker result log in both human and JSON
// modes. Round 1 shipped only `mission result` (the write path);
// the sibling read path was MCP-only. mdm flagged the asymmetry
// against `mission reflect`/`mission reflections`.
func TestMissionResults_ListsSubmittedResults(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	// Empty case — no results yet, JSON mode must produce "[]"
	// (never "null") so consumers can unmarshal into []Result
	// without a nil guard.
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() { runMissionResults(id) })
	assert.Equal(t, "[]", strings.TrimSpace(out))

	// Submit a result and fetch again.
	submitCLIResult(t, id, 1)
	out = captureStdout(t, func() { runMissionResults(id) })
	var got []mission.Result
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 1)
	assert.Equal(t, 1, got[0].Round)
	assert.Equal(t, mission.VerdictPass, got[0].Verdict)
	assert.Equal(t, id, got[0].Mission)

	// Human mode: the Results section header and a round 1 bullet
	// must appear.
	jsonOutput = false
	humanOut := captureStdout(t, func() { runMissionResults(id) })
	assert.Contains(t, humanOut, "Results:")
	assert.Contains(t, humanOut, "round 1")
	assert.Contains(t, humanOut, "pass")
}

// TestMissionResults_HelpListsSubcommand asserts the H3 discovery
// fix: the `results` subcommand appears in `ethos mission --help`
// so the operator can find it. Without this, the subcommand is a
// ghost feature — present but undocumented.
func TestMissionResults_HelpListsSubcommand(t *testing.T) {
	missionTestEnv(t)
	stdout, _, err := runCobra(t, "mission")
	require.NoError(t, err)
	assert.Contains(t, stdout, "results")
	assert.Contains(t, stdout, "Show the round-by-round result log")
}

// TestMissionResult_RefusesInvalidShapeNamesFilePath asserts the
// M1 fix: a structural Validate failure (empty verdict,
// out-of-range confidence, empty evidence) produces an error that
// includes the --file path so the operator can locate the source
// of the failure in one pass. Runs in a subprocess because
// runMissionResult calls os.Exit on validation failure.
func TestMissionResult_RefusesInvalidShapeNamesFilePath(t *testing.T) {
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
  - tests/m1-file-path/
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

	// Write a result with an invalid verdict — structural
	// Validate failure. The error must name the file path.
	badFile := filepath.Join(tmp, "bad-result.yaml")
	body := fmt.Sprintf(`mission: %s
round: 1
author: bwk
verdict: ""
confidence: 0.5
files_changed:
  - path: tests/m1-file-path/
    added: 1
    removed: 0
evidence:
  - name: make check
    status: pass
`, id)
	require.NoError(t, os.WriteFile(badFile, []byte(body), 0o600))

	resultCmd := exec.Command(ethosBinary, "mission", "result", id, "--file", badFile)
	resultCmd.Env = env
	var stderrBuf bytes.Buffer
	resultCmd.Stderr = &stderrBuf
	err := resultCmd.Run()
	require.Error(t, err)
	stderr := stderrBuf.String()
	assert.Contains(t, stderr, badFile,
		"error must name the --file path, got: %s", stderr)
	assert.Contains(t, stderr, "invalid verdict",
		"error must still carry the Validate diagnostic, got: %s", stderr)
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

// TestMissionClose_JSON asserts the JSON output shape for the CLI
// close subcommand. The payload must surface the round and verdict
// that authorized the close so a scripting caller does not need a
// follow-up `mission log` to learn which result satisfied the gate.
func TestMissionClose_JSON(t *testing.T) {
	missionTestEnv(t)
	jsonOutput = true
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	submitCLIResult(t, id, 1)

	out := captureStdout(t, func() { runMissionClose(id, mission.StatusClosed) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, id, got["mission_id"])
	assert.Equal(t, float64(1), got["round"])
	assert.Equal(t, "pass", got["verdict"])
	assert.Equal(t, mission.StatusClosed, got["status"])
}

// TestMissionAdvance_JSON asserts the JSON output shape for the CLI
// advance subcommand. The payload must surface the new current
// round so a scripting caller does not need a follow-up `mission
// show` to learn the round transition.
func TestMissionAdvance_JSON(t *testing.T) {
	missionTestEnv(t)
	jsonOutput = true
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	missionReflectFile = writeReflectionFile(t, 1, "continue", "ok")
	captureStdout(t, func() { runMissionReflect(id, missionReflectFile) })

	out := captureStdout(t, func() { runMissionAdvance(id) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, id, got["mission_id"])
	assert.Equal(t, float64(1), got["from_round"])
	assert.Equal(t, float64(2), got["to_round"])
	assert.Equal(t, float64(2), got["current_round"])
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

// --- Phase 3.7: mission log ---
//
// Classes 14-23 from the Phase 3.7 failure-mode table:
//
//   14 — --event foo with no matching events
//   15 — --event foo,bar with partial matches
//   16 — --since <future> with no matching events
//   17 — --since <past> includes all
//   18 — --event X --since Y AND-composed
//   19 — unknown event type string in --event (accepted, empty result)
//   20 — `ethos mission log` (no id) errors with usage
//   21 — `ethos mission log <prefix>` prefix match
//   22 — `ethos mission log <unknown-id>` errors
//   23 — `ethos mission log <id> --json` empty is [] not null
//
// Classes 14-19 are filter-interaction through the CLI surface so
// they exercise parseEventTypes + FilterEvents + runMissionLog end-
// to-end. Pure filter unit tests live in internal/mission/log_test.go.

// seedMissionWithEvents creates a single mission via the CLI create
// path, then drives it through result + close so the on-disk log
// carries create + result + close events. Returns the mission ID.
func seedMissionWithEvents(t *testing.T) string {
	t.Helper()
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)
	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]
	submitCLIResult(t, id, 1)
	captureStdout(t, func() { runMissionClose(id, mission.StatusClosed) })
	return id
}

func TestMissionLog_CleanLogRoundTrip(t *testing.T) {
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() {
		runMissionLog(id, "", "")
	})
	var payload mission.LogPayload
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.Len(t, payload.Events, 3)
	assert.Equal(t, "create", payload.Events[0].Event)
	assert.Equal(t, "result", payload.Events[1].Event)
	assert.Equal(t, "close", payload.Events[2].Event)
	assert.Empty(t, payload.Warnings)
}

func TestMissionLog_EventFilter_NoMatch(t *testing.T) {
	// Class 14: --event foo with no matching events.
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() {
		runMissionLog(id, "foo", "")
	})
	var payload mission.LogPayload
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	assert.Empty(t, payload.Events)
	// A2 regression guard: must be [], not null.
	assert.Contains(t, out, `"events": []`)
	assert.NotContains(t, out, `"events": null`)
}

func TestMissionLog_EventFilter_PartialMatch(t *testing.T) {
	// Class 15: --event create,close with partial matches.
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() {
		runMissionLog(id, "create,close", "")
	})
	var payload mission.LogPayload
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.Len(t, payload.Events, 2)
	assert.Equal(t, "create", payload.Events[0].Event)
	assert.Equal(t, "close", payload.Events[1].Event)
}

func TestMissionLog_SinceFilter_Future(t *testing.T) {
	// Class 16: --since <future> with no matching events.
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() {
		runMissionLog(id, "", "2099-01-01T00:00:00Z")
	})
	var payload mission.LogPayload
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	assert.Empty(t, payload.Events)
	assert.Contains(t, out, `"events": []`)
}

func TestMissionLog_SinceFilter_Past(t *testing.T) {
	// Class 17: --since <past> includes all.
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() {
		runMissionLog(id, "", "2020-01-01T00:00:00Z")
	})
	var payload mission.LogPayload
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.Len(t, payload.Events, 3)
}

func TestMissionLog_BothFilters_ANDComposed(t *testing.T) {
	// Class 18: --event X --since Y AND-composed.
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	// Filter for `result` events since epoch — only the result row
	// survives both gates.
	out := captureStdout(t, func() {
		runMissionLog(id, "result", "2020-01-01T00:00:00Z")
	})
	var payload mission.LogPayload
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.Len(t, payload.Events, 1)
	assert.Equal(t, "result", payload.Events[0].Event)
}

func TestMissionLog_UnknownEventType_IsAcceptedNotRejected(t *testing.T) {
	// Class 19: an unknown event type string in --event is accepted
	// and returns empty. The flag parser does not validate against
	// a closed enum — event types are forward-compatible.
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() {
		runMissionLog(id, "worker_spawned", "")
	})
	var payload mission.LogPayload
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	assert.Empty(t, payload.Events)
	assert.Contains(t, out, `"events": []`)
}

func TestMissionLog_NoID_ErrorsWithUsage(t *testing.T) {
	// Class 20: `ethos mission log` with no id errors with the cobra
	// usage hint, exit non-zero. Use the runCobra helper to drive
	// the full cobra path including arg-count validation.
	missionTestEnv(t)
	_, stderr, err := runCobra(t, "mission", "log")
	require.Error(t, err)
	assert.Contains(t, stderr, "accepts 1 arg")
}

func TestMissionLog_PrefixMatch(t *testing.T) {
	// Class 21: `ethos mission log <prefix>` prefix match via
	// MatchByPrefix, symmetric with mission show.
	missionTestEnv(t)
	id := seedMissionWithEvents(t)
	// Use the first 12 characters (the "m-2026-04-NN" prefix) as an
	// unambiguous prefix — only one mission in the store.
	prefix := id[:12]
	require.NotEqual(t, prefix, id, "prefix must be shorter than full id")

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() {
		runMissionLog(prefix, "", "")
	})
	var payload mission.LogPayload
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	assert.Len(t, payload.Events, 3)
}

func TestMissionLog_UnknownID_Errors(t *testing.T) {
	// Class 22: `ethos mission log <unknown-id>` errors. Runs in a
	// subprocess because runMissionLog calls os.Exit on resolution
	// failure.
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}
	home := t.TempDir()
	seedEvaluator(t, filepath.Join(home, ".punt-labs", "ethos"))
	cmd := exec.Command(ethosBinary, "mission", "log", "m-unknown-999")
	cmd.Env = append(os.Environ(), "HOME="+home)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	require.Error(t, err, "unknown mission id must exit non-zero")
	assert.Contains(t, stderrBuf.String(), "no mission matching prefix")
}

func TestMissionLog_EmptyJSON_IsEmptyArrayNotNull(t *testing.T) {
	// Class 23: `mission log <id> --json` with zero matching events
	// returns `"events": []`, never `"events": null`. Phase 3.6 A2
	// regression guard: a typed-nil slice boxed into a map was the
	// exact bug mdm caught on show; the same trap applies here.
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	// Filter out every event so the payload is empty.
	out := captureStdout(t, func() {
		runMissionLog(id, "no-such-event", "")
	})
	assert.Contains(t, out, `"events": []`)
	assert.NotContains(t, out, `"events": null`)
	// Warnings must be omitted on a clean log.
	assert.NotContains(t, out, `"warnings"`)
}

func TestMissionLog_HumanMode_RendersAllEvents(t *testing.T) {
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = false
	out := captureStdout(t, func() {
		runMissionLog(id, "", "")
	})
	assert.Contains(t, out, "Events:")
	assert.Contains(t, out, "create")
	assert.Contains(t, out, "result")
	assert.Contains(t, out, "close")
}

func TestMissionLog_HumanMode_Empty_RendersNone(t *testing.T) {
	missionTestEnv(t)
	id := seedMissionWithEvents(t)
	jsonOutput = false
	out := captureStdout(t, func() {
		runMissionLog(id, "no-such-event", "")
	})
	assert.Contains(t, out, "Events:")
	assert.Contains(t, out, "(none)")
}

func TestMissionLog_InvalidSinceFlag_SubprocessExits(t *testing.T) {
	// An invalid --since is a fatal error from FilterEvents; the
	// operator sees the bad value named in the error. Runs in a
	// subprocess because runMissionLog calls os.Exit on the filter
	// error path.
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
  bead: ethos-07m.11
write_set:
  - tests/log-bad-since/
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

	cmd := exec.Command(ethosBinary, "mission", "log", id, "--since", "not-a-timestamp")
	cmd.Env = env
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	require.Error(t, err, "invalid --since must exit non-zero")
	assert.Contains(t, stderrBuf.String(), "since")
	assert.Contains(t, stderrBuf.String(), "not-a-timestamp")
}

func TestMissionLog_CorruptLineSurfacesAsWarning(t *testing.T) {
	// Drive a clean mission, then plant a corrupt line in the middle
	// of the JSONL log. Reading it back via --json must surface a
	// warnings field naming the bad line and still return the
	// parseable events.
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	// The CLI uses the bare missionStore() path which reads HOME,
	// so the sandbox HOME is the one missionTestEnv set.
	home := os.Getenv("HOME")
	logPath := filepath.Join(home, ".punt-labs", "ethos", "missions", id+".jsonl")
	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 3, "expected at least create+result+close")
	// Insert a garbage line between line 1 (create) and line 2.
	corrupted := []string{lines[0], "{garbage", lines[1], lines[2]}
	require.NoError(t, os.WriteFile(logPath, []byte(strings.Join(corrupted, "\n")+"\n"), 0o600))

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() {
		runMissionLog(id, "", "")
	})
	var payload mission.LogPayload
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	// The three good lines still decode.
	assert.Len(t, payload.Events, 3)
	// Warnings name the corrupt line number.
	require.Len(t, payload.Warnings, 1)
	assert.Contains(t, payload.Warnings[0], "line 2")
}

func TestMissionLog_HelpListsSubcommand(t *testing.T) {
	missionTestEnv(t)
	stdout, _, err := runCobra(t, "mission")
	require.NoError(t, err)
	assert.Contains(t, stdout, "log")
	assert.Contains(t, stdout, "Show the append-only mission event log")
}

// --- Round 2 regression guards ---

// TestMissionLog_HumanMode_BulletPrefix covers M1: the CLI
// printEventLog now emits `  - ` before each event row, matching
// the MCP formatter walker and the sibling subcommands (mission
// show, mission results, mission reflections). Round 1 shipped
// without the dash, which mdm flagged as family drift.
func TestMissionLog_HumanMode_BulletPrefix(t *testing.T) {
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	jsonOutput = false
	out := captureStdout(t, func() {
		runMissionLog(id, "", "")
	})
	// Every rendered event line must carry the "  - " prefix. Grep
	// the output for lines that mention an event type but lack the
	// dash — any such line is a regression.
	lines := strings.Split(out, "\n")
	var eventLines int
	for _, line := range lines {
		if strings.Contains(line, "create") ||
			strings.Contains(line, "result") ||
			strings.Contains(line, "close") {
			if strings.HasPrefix(line, "Events:") {
				continue
			}
			eventLines++
			assert.True(t, strings.HasPrefix(line, "  - "),
				"event line must begin with bullet prefix, got: %q", line)
		}
	}
	assert.GreaterOrEqual(t, eventLines, 3, "must render at least create+result+close rows")
}

// TestMissionLog_HumanMode_WarningsFooterOnStdout covers M2: when
// the on-disk log has a corrupt line, the warnings print as a
// trailing Warnings section on stdout (not stderr). Round 1
// routed warnings to stderr only, which hid damage from any
// `ethos mission log > events.txt` consumer. The footer format
// matches the MCP walker's convention.
func TestMissionLog_HumanMode_WarningsFooterOnStdout(t *testing.T) {
	missionTestEnv(t)
	id := seedMissionWithEvents(t)

	home := os.Getenv("HOME")
	logPath := filepath.Join(home, ".punt-labs", "ethos", "missions", id+".jsonl")
	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	corrupted := []string{lines[0], "{garbage", lines[1], lines[2]}
	require.NoError(t, os.WriteFile(logPath, []byte(strings.Join(corrupted, "\n")+"\n"), 0o600))

	jsonOutput = false
	out := captureStdout(t, func() {
		runMissionLog(id, "", "")
	})
	// The events section still renders the three good lines.
	assert.Contains(t, out, "Events:")
	assert.Contains(t, out, "create")
	// The warnings footer must appear on stdout, naming the bad line.
	assert.Contains(t, out, "Warnings:")
	assert.Contains(t, out, "line 2")
	// Warnings bullets must use the same prefix as the events.
	assert.Regexp(t, `(?m)^  - line 2`, out)
}

// TestMissionLog_InvalidSinceFlag_HumanReadableError covers L1:
// the error message for an invalid --since value must name the
// bad input and offer an RFC3339 hint without leaking the Go
// time reference layout string. Round 1 forwarded the bare
// time.Parse error which included "2006-01-02T15:04:05Z07:00".
func TestMissionLog_InvalidSinceFlag_HumanReadableError(t *testing.T) {
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
  bead: ethos-07m.11
write_set:
  - tests/log-bad-since-hr/
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

	cmd := exec.Command(ethosBinary, "mission", "log", id, "--since", "tomorrow")
	cmd.Env = env
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	require.Error(t, err, "invalid --since must exit non-zero")
	msg := stderrBuf.String()
	assert.Contains(t, msg, "tomorrow", "error must name the bad input")
	assert.Contains(t, msg, "RFC3339", "error must suggest RFC3339")
	assert.NotContains(t, msg, "2006-01-02", "Go layout reference must not leak")
	assert.NotContains(t, msg, "Z07:00", "Go layout reference must not leak")
}

// TestMissionLog_Help_DocumentsJSONShape covers M5: the --help
// long text now documents the wrapped `{"events": [...],
// "warnings": [...]}` shape so an operator doing
// `ethos mission log $id --json | jq '.[]'` understands why the
// shape departs from the bare-array siblings.
func TestMissionLog_Help_DocumentsJSONShape(t *testing.T) {
	missionTestEnv(t)
	stdout, _, err := runCobra(t, "mission", "log", "--help")
	require.NoError(t, err)
	assert.Contains(t, stdout, "JSON output shape")
	assert.Contains(t, stdout, `{"events"`)
	assert.Contains(t, stdout, `"warnings"`)
}

// TestMissionLog_Help_DocumentsEmptyEventFilter covers L4: the
// help text explicitly notes that --event "" (empty string) or
// an omitted flag returns all event types — the silent-degradation
// case a scripted consumer could hit on an empty
// user-supplied value.
func TestMissionLog_Help_DocumentsEmptyEventFilter(t *testing.T) {
	missionTestEnv(t)
	stdout, _, err := runCobra(t, "mission", "log", "--help")
	require.NoError(t, err)
	// The help text mentions "empty" + "all event types" somewhere
	// in the --event description.
	assert.Contains(t, strings.ToLower(stdout), "empty")
	assert.Contains(t, strings.ToLower(stdout), "all event types")
}

// --- Phase 3.? ethos-2e4: `mission result --verify` ---
//
// The following tests cover the CLI-side cross-check that diffs the
// worker's declared files_changed counts against `git diff --numstat`.
// Five of the six are subprocess tests because they need a real git
// repo plus a real binary invocation; test #1 (verify OFF, default
// behavior unchanged) runs in-process because no git is involved.
//
// All subprocess tests reuse seedEvaluator + the canonical contract
// body to bring a mission into existence, then drive `mission result`
// inside a temp git repo where `git diff --numstat HEAD~1..HEAD`
// returns a deterministic payload. Using HEAD~1 as the base sidesteps
// the need for a feature-branch fixture and exercises the --base
// override path.

// testGitEnv returns a child-process environment suitable for
// running git in a test fixture: global and system config are
// redirected to /dev/null, GPG signing is forced off, and a
// deterministic author/committer is set. The function also strips
// every inherited GIT_CONFIG_* entry so the developer's shell
// .envrc (which injects signing key bindings via GIT_CONFIG_COUNT
// in this repo) cannot contaminate the child's view of git config.
func testGitEnv(home string) []string {
	base := make([]string, 0, len(os.Environ())+10)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_CONFIG_") ||
			strings.HasPrefix(kv, "GIT_AUTHOR_") ||
			strings.HasPrefix(kv, "GIT_COMMITTER_") ||
			strings.HasPrefix(kv, "HOME=") {
			continue
		}
		base = append(base, kv)
	}
	return append(base,
		"HOME="+home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign",
		"GIT_CONFIG_VALUE_0=false",
	)
}

// missionResultVerifyEnv bootstraps the subprocess fixture common to
// every --verify test: a temp HOME with the evaluator identity
// seeded, a temp git repo with an initial commit and a second commit
// that modifies two files (and adds a third), and a created mission
// whose write_set admits all three paths. Returns the temp HOME, the
// git-repo working directory, and the mission ID.
//
// The second commit is shaped so `git diff --numstat HEAD~1..HEAD`
// yields exactly:
//
//	5\t0\ta.txt
//	3\t1\tb.txt
//	3\t0\tc.txt
//
// so every --verify test references the same known-good counts.
func missionResultVerifyEnv(t *testing.T) (home, repo, missionID string) {
	t.Helper()
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	home = t.TempDir()
	seedEvaluator(t, filepath.Join(home, ".punt-labs", "ethos"))

	repo = t.TempDir()
	env := testGitEnv(home)

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s failed: %s", strings.Join(args, " "), out)
	}

	runGit("init", "-b", "main", repo)

	// Baseline commit: a.txt has 5 lines, b.txt has 3 lines.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"),
		[]byte("a1\na2\na3\na4\na5\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "b.txt"),
		[]byte("b1\nb2\nb3\n"), 0o600))
	runGit("add", "a.txt", "b.txt")
	runGit("commit", "-m", "baseline")

	// Change commit: a.txt adds 5 lines (0 removed), b.txt adds 3
	// lines and removes 1 (net +2), c.txt is new with 3 lines.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"),
		[]byte("a1\na2\na3\na4\na5\na6\na7\na8\na9\na10\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "b.txt"),
		[]byte("b1\nb2-modified\nb3\nb4\nb5\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "c.txt"),
		[]byte("c1\nc2\nc3\n"), 0o600))
	runGit("add", "a.txt", "b.txt", "c.txt")
	runGit("commit", "-m", "changes")

	// Contract body admits all three paths touched by the change
	// commit. result.yaml is untracked scratch and never appears in
	// numstat, so the write_set does not need to name it.
	contract := filepath.Join(repo, "contract.yaml")
	require.NoError(t, os.WriteFile(contract, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-2e4
write_set:
  - a.txt
  - b.txt
  - c.txt
success_criteria:
  - make check passes
budget:
  rounds: 3
`), 0o600))

	runEthos := func(args ...string) (string, string) {
		cmd := exec.Command(ethosBinary, args...)
		cmd.Env = env
		cmd.Dir = repo
		var out, errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		require.NoError(t, cmd.Run(),
			"ethos %s failed: %s", strings.Join(args, " "), errBuf.String())
		return out.String(), errBuf.String()
	}

	runEthos("mission", "create", "--file", contract)
	listOut, _ := runEthos("mission", "list", "--json")
	var entries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(listOut), &entries))
	require.Len(t, entries, 1)
	missionID, _ = entries[0]["mission_id"].(string)
	return home, repo, missionID
}

// writeVerifyResultFile drops a result YAML at repo/result.yaml with
// the given files_changed entries and returns the path. The evidence
// block is fixed — every verify test just needs a well-formed result
// that names the files_changed it is asserting against.
func writeVerifyResultFile(t *testing.T, repo, missionID string, files []mission.FileChange) string {
	t.Helper()
	var fc strings.Builder
	for _, f := range files {
		fmt.Fprintf(&fc, "  - path: %s\n    added: %d\n    removed: %d\n",
			f.Path, f.Added, f.Removed)
	}
	body := fmt.Sprintf(`mission: %s
round: 1
author: bwk
verdict: pass
confidence: 0.9
files_changed:
%sevidence:
  - name: make check
    status: pass
`, missionID, fc.String())
	path := filepath.Join(repo, "result.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// TestMissionResult_VerifyOff_DefaultBehaviorUnchanged asserts that a
// result with deliberately wrong counts still lands when --verify is
// not set. The in-process path is sufficient here because no git
// invocation should happen at all — the test is proving the verify
// code is gated behind the flag.
func TestMissionResult_VerifyOff_DefaultBehaviorUnchanged(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	// Write a result with clearly fabricated counts for a path that
	// lives inside the contract write_set. Without --verify, the
	// mission store accepts it — the line counts are advisory in the
	// schema.
	dir := t.TempDir()
	path := filepath.Join(dir, "result.yaml")
	body := fmt.Sprintf(`mission: %s
round: 1
author: bwk
verdict: pass
confidence: 0.9
files_changed:
  - path: internal/mission/result.go
    added: 99999
    removed: 99999
evidence:
  - name: make check
    status: pass
`, id)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	missionResultFile = path
	missionResultVerify = false
	out := captureStdout(t, func() { runMissionResult(id, path) })
	assert.Contains(t, out, "result:")
	assert.Contains(t, out, "round=1")
	assert.Contains(t, out, "verdict=pass")
}

// TestMissionResult_VerifyOn_PassesOnMatch drives the subprocess
// through a real git repo and asserts that declared counts matching
// the real numstat are accepted.
func TestMissionResult_VerifyOn_PassesOnMatch(t *testing.T) {
	home, repo, id := missionResultVerifyEnv(t)
	resultFile := writeVerifyResultFile(t, repo, id, []mission.FileChange{
		{Path: "a.txt", Added: 5, Removed: 0},
		{Path: "b.txt", Added: 3, Removed: 1},
		{Path: "c.txt", Added: 3, Removed: 0},
	})

	cmd := exec.Command(ethosBinary,
		"mission", "result", id,
		"--file", resultFile,
		"--verify", "--base", "HEAD~1")
	cmd.Env = testGitEnv(home)
	cmd.Dir = repo
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	require.NoError(t, cmd.Run(),
		"verify must accept matching counts: stderr=%s", errBuf.String())
	assert.Contains(t, outBuf.String(), "result:")
	assert.NotContains(t, errBuf.String(), "warning",
		"all diff paths were declared; no warning expected")
}

// TestMissionResult_VerifyOn_RejectsOnMismatch asserts that a
// declared count that disagrees with git numstat produces a clean
// rejection naming the file and both count pairs.
func TestMissionResult_VerifyOn_RejectsOnMismatch(t *testing.T) {
	home, repo, id := missionResultVerifyEnv(t)
	// a.txt is declared with the WRONG added count; everything else
	// matches. The helper must name a.txt and both count pairs so
	// the operator can read the discrepancy without re-running git.
	resultFile := writeVerifyResultFile(t, repo, id, []mission.FileChange{
		{Path: "a.txt", Added: 42, Removed: 0},
		{Path: "b.txt", Added: 3, Removed: 1},
		{Path: "c.txt", Added: 3, Removed: 0},
	})

	cmd := exec.Command(ethosBinary,
		"mission", "result", id,
		"--file", resultFile,
		"--verify", "--base", "HEAD~1")
	cmd.Env = testGitEnv(home)
	cmd.Dir = repo
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	require.Error(t, err, "mismatched counts must fail")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected ExitError, got %T: %v", err, err)
	assert.Equal(t, 1, exitErr.ExitCode())

	stderr := errBuf.String()
	assert.Contains(t, stderr, "a.txt", "error must name the mismatched path")
	assert.Contains(t, stderr, "added=42", "error must name the declared count")
	assert.Contains(t, stderr, "added=5", "error must name the real count")
	assert.Contains(t, stderr, "--verify",
		"error must identify the --verify path so operator knows which flag caused it")
}

// TestMissionResult_VerifyOn_RejectsUnknownPath asserts the
// "declared but not in diff" branch: declare a file that the
// write_set admits but that the diff range does not touch. Uses a
// dedicated fixture because the shared env touches every admitted
// path.
func TestMissionResult_VerifyOn_RejectsUnknownPath(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	home := t.TempDir()
	seedEvaluator(t, filepath.Join(home, ".punt-labs", "ethos"))

	repo := t.TempDir()
	env := testGitEnv(home)
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	}
	runGit("init", "-b", "main", repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"),
		[]byte("a1\na2\n"), 0o600))
	runGit("add", "a.txt")
	runGit("commit", "-m", "baseline")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"),
		[]byte("a1\na2\na3\n"), 0o600))
	runGit("add", "a.txt")
	runGit("commit", "-m", "change a only")

	contract := filepath.Join(repo, "contract.yaml")
	require.NoError(t, os.WriteFile(contract, []byte(`leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-2e4
write_set:
  - a.txt
  - ghost.txt
success_criteria:
  - make check passes
budget:
  rounds: 3
`), 0o600))

	runEthos := func(args ...string) string {
		cmd := exec.Command(ethosBinary, args...)
		cmd.Env = testGitEnv(home)
		cmd.Dir = repo
		var out, errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		require.NoError(t, cmd.Run(), "ethos %s: %s",
			strings.Join(args, " "), errBuf.String())
		return out.String()
	}
	runEthos("mission", "create", "--file", contract)
	listOut := runEthos("mission", "list", "--json")
	var entries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(listOut), &entries))
	id, _ := entries[0]["mission_id"].(string)

	// ghost.txt is write_set-admitted but not in the diff range.
	resultFile := filepath.Join(repo, "result.yaml")
	body := fmt.Sprintf(`mission: %s
round: 1
author: bwk
verdict: pass
confidence: 0.9
files_changed:
  - path: a.txt
    added: 1
    removed: 0
  - path: ghost.txt
    added: 10
    removed: 0
evidence:
  - name: make check
    status: pass
`, id)
	require.NoError(t, os.WriteFile(resultFile, []byte(body), 0o600))

	cmd := exec.Command(ethosBinary,
		"mission", "result", id,
		"--file", resultFile,
		"--verify", "--base", "HEAD~1")
	cmd.Env = testGitEnv(home)
	cmd.Dir = repo
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	require.Error(t, err, "absent path must be rejected")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected ExitError, got %T: %v", err, err)
	assert.Equal(t, 1, exitErr.ExitCode())

	stderr := errBuf.String()
	assert.Contains(t, stderr, "ghost.txt", "error must name the undeclared path")
	assert.Contains(t, stderr, "not in",
		"error must explain that the path is missing from the diff")
}

// TestMissionResult_VerifyOn_WarnsOnUndeclaredDiffPath asserts that
// a file present in the numstat diff but not declared in
// files_changed emits a warning on stderr but does not reject — the
// leader may legitimately omit auto-generated files from the
// result accounting.
func TestMissionResult_VerifyOn_WarnsOnUndeclaredDiffPath(t *testing.T) {
	home, repo, id := missionResultVerifyEnv(t)
	// Declare only a.txt and b.txt; c.txt is in the diff but omitted
	// on purpose.
	resultFile := writeVerifyResultFile(t, repo, id, []mission.FileChange{
		{Path: "a.txt", Added: 5, Removed: 0},
		{Path: "b.txt", Added: 3, Removed: 1},
	})

	cmd := exec.Command(ethosBinary,
		"mission", "result", id,
		"--file", resultFile,
		"--verify", "--base", "HEAD~1")
	cmd.Env = testGitEnv(home)
	cmd.Dir = repo
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	require.NoError(t, cmd.Run(),
		"undeclared diff path must warn, not reject: stderr=%s", errBuf.String())

	assert.Contains(t, outBuf.String(), "result:",
		"stdout must still carry the success echo")
	stderr := errBuf.String()
	assert.Contains(t, stderr, "warning")
	assert.Contains(t, stderr, "c.txt")
	assert.Contains(t, stderr, "not declared")
}

// TestMissionResult_VerifyOn_RejectsInvalidBase asserts that a
// --base ref that git rev-parse cannot resolve produces a clean
// error naming the bad ref, not a cryptic git exit code.
func TestMissionResult_VerifyOn_RejectsInvalidBase(t *testing.T) {
	home, repo, id := missionResultVerifyEnv(t)
	resultFile := writeVerifyResultFile(t, repo, id, []mission.FileChange{
		{Path: "a.txt", Added: 5, Removed: 0},
	})

	cmd := exec.Command(ethosBinary,
		"mission", "result", id,
		"--file", resultFile,
		"--verify", "--base", "nonexistent-ref-xyz")
	cmd.Env = testGitEnv(home)
	cmd.Dir = repo
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	require.Error(t, err, "invalid --base ref must fail")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected ExitError, got %T: %v", err, err)
	assert.Equal(t, 1, exitErr.ExitCode())

	stderr := errBuf.String()
	assert.Contains(t, stderr, "nonexistent-ref-xyz",
		"error must name the bad ref")
	assert.Contains(t, stderr, "base ref",
		"error must identify the failing input as the base ref")
}

// TestMissionResult_VerifyOn_RejectsFlaglikeBase asserts the end-to-end
// invariant that a --base value shaped like a git flag (e.g.
// --output=<path>) cannot cause git to create a file on disk. Without
// `--end-of-options` sandboxing the base argument, `git diff --numstat
// --output=<path>..HEAD` would silently write to <path>; this is the
// argument-injection class Bugbot flagged. The fix prepends
// `--end-of-options` to both `git rev-parse --verify` and `git diff
// --numstat` so git treats the base as a positional revision.
//
// Under the current code path the attack is blocked at the `rev-parse
// --verify` gate — rev-parse has no --output flag and rejects any
// flag-shaped base as an unresolvable revision. The diff-site
// `--end-of-options` is therefore defense in depth: it maintains the
// invariant if the rev-parse gate is ever relaxed or reordered. The
// test asserts the end-to-end invariant (no file on disk) rather than
// which layer enforces it, so it will hold the line against both
// layers shifting in the future.
func TestMissionResult_VerifyOn_RejectsFlaglikeBase(t *testing.T) {
	home, repo, id := missionResultVerifyEnv(t)
	resultFile := writeVerifyResultFile(t, repo, id, []mission.FileChange{
		{Path: "a.txt", Added: 5, Removed: 0},
	})

	// Probe lives in an isolated TempDir so no other test can
	// accidentally create or clean it. The TempDir itself exists;
	// the probe file must not, before or after the test.
	probeDir := t.TempDir()
	probe := filepath.Join(probeDir, "injection")
	require.NoFileExists(t, probe, "probe must not exist before the test")

	cmd := exec.Command(ethosBinary,
		"mission", "result", id,
		"--file", resultFile,
		"--verify", "--base", "--output="+probe)
	cmd.Env = testGitEnv(home)
	cmd.Dir = repo
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	require.Error(t, err, "flaglike --base must fail, not silently succeed")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected ExitError, got %T: %v", err, err)
	assert.Equal(t, 1, exitErr.ExitCode(),
		"ethos must exit 1 on flaglike --base; stderr=%s", errBuf.String())

	// The load-bearing invariant: no file on disk. If --end-of-options
	// is missing from either subprocess call site, git would write
	// here and this assertion would fail.
	assert.NoFileExists(t, probe,
		"flaglike --base must not cause git to write the probe file")
}

// TestMissionResult_VerifyOn_PassesEndOfOptionsToGit asserts positively
// that both subprocess call sites — `git rev-parse --verify` and
// `git diff --numstat` — pass `--end-of-options` immediately before the
// base argument. The test prepends a shim directory to PATH with a
// `git` wrapper that records every argv to a log file and then forwards
// to the real git binary. After a successful `ethos mission result
// --verify` run, the log is scanned for the two expected invocations
// and the `--end-of-options` separator is asserted on each.
//
// This complements the end-to-end no-file-on-disk test by proving the
// separator is actually sent on the wire, so a future edit that removes
// the flag from either call site fails here even when the rev-parse
// gate would otherwise hide the regression.
func TestMissionResult_VerifyOn_PassesEndOfOptionsToGit(t *testing.T) {
	home, repo, id := missionResultVerifyEnv(t)
	resultFile := writeVerifyResultFile(t, repo, id, []mission.FileChange{
		{Path: "a.txt", Added: 5, Removed: 0},
		{Path: "b.txt", Added: 3, Removed: 1},
		{Path: "c.txt", Added: 3, Removed: 0},
	})

	// The shim logs one line per invocation, tab-separated argv.
	// Forwarding to the real git preserves end-to-end behavior so the
	// rest of the verify pipeline runs unmodified.
	shimDir := t.TempDir()
	logFile := filepath.Join(shimDir, "argv.log")
	realGit, err := exec.LookPath("git")
	require.NoError(t, err, "real git not found on PATH")
	shimBody := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
exec %q "$@"
`, logFile, realGit)
	shimPath := filepath.Join(shimDir, "git")
	require.NoError(t, os.WriteFile(shimPath, []byte(shimBody), 0o700))

	env := testGitEnv(home)
	// Prepend the shim directory to PATH so `git` resolves to the
	// shim rather than /usr/bin/git.
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + shimDir + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
			break
		}
	}

	cmd := exec.Command(ethosBinary,
		"mission", "result", id,
		"--file", resultFile,
		"--verify", "--base", "HEAD~1")
	cmd.Env = env
	cmd.Dir = repo
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	require.NoError(t, cmd.Run(),
		"verify must succeed under the git shim: stderr=%s", errBuf.String())

	log, err := os.ReadFile(logFile)
	require.NoError(t, err, "shim log must exist")
	lines := strings.Split(strings.TrimRight(string(log), "\n"), "\n")

	// Find the two invocations we care about. Other git calls may
	// appear in the log (e.g. config reads) and are ignored.
	var revParseLine, diffLine string
	for _, line := range lines {
		if strings.HasPrefix(line, "rev-parse --verify ") {
			revParseLine = line
		}
		if strings.HasPrefix(line, "diff --numstat ") {
			diffLine = line
		}
	}
	require.NotEmpty(t, revParseLine,
		"expected a `git rev-parse --verify ...` invocation in shim log:\n%s", log)
	require.NotEmpty(t, diffLine,
		"expected a `git diff --numstat ...` invocation in shim log:\n%s", log)

	// The separator must appear before the base argument. A regression
	// that drops it from either call site fails here.
	assert.Contains(t, revParseLine, "--end-of-options HEAD~1",
		"rev-parse must pass --end-of-options immediately before base; got: %s", revParseLine)
	assert.Contains(t, diffLine, "--end-of-options HEAD~1..HEAD",
		"git diff must pass --end-of-options immediately before base; got: %s", diffLine)
}

// TestMissionResult_VerifyOn_AcceptsCanonicallyEquivalentPaths asserts
// that a worker who declares `./a.txt` in files_changed — which the
// write_set validator already accepts because `./a.txt` and `a.txt`
// normalize to the same segment list — is not falsely rejected by
// --verify against `git diff --numstat`, which emits the canonical
// `a.txt`. The verify helper must compare paths using the same
// canonicalization the validator uses; the two were briefly out of
// sync in ethos-2e4 round 1 and Copilot caught it.
func TestMissionResult_VerifyOn_AcceptsCanonicallyEquivalentPaths(t *testing.T) {
	home, repo, id := missionResultVerifyEnv(t)
	// Declare every path in a form that normalizes equal to the
	// canonical git output but is textually different:
	//   `./a.txt`   — leading `./`
	//   `b.txt/`    — trailing slash
	//   `./c.txt`   — leading `./`
	// All three must be accepted.
	resultFile := writeVerifyResultFile(t, repo, id, []mission.FileChange{
		{Path: "./a.txt", Added: 5, Removed: 0},
		{Path: "b.txt/", Added: 3, Removed: 1},
		{Path: "./c.txt", Added: 3, Removed: 0},
	})

	cmd := exec.Command(ethosBinary,
		"mission", "result", id,
		"--file", resultFile,
		"--verify", "--base", "HEAD~1")
	cmd.Env = testGitEnv(home)
	cmd.Dir = repo
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	require.NoError(t, cmd.Run(),
		"verify must accept canonically equivalent paths: stderr=%s", errBuf.String())
	assert.Contains(t, outBuf.String(), "result:")
	assert.NotContains(t, errBuf.String(), "warning",
		"every diff path matches a declared path after canonicalization; no warning expected")
}

// TestMissionResult_VerifyOn_UndeclaredWarningUsesCanonicalComparison
// asserts the "undeclared diff path" warning loop also uses canonical
// comparison. A worker who declares `./c.txt` must not see a warning
// about `c.txt` — the paths match canonically. Without the canonical
// comparison on both sides the warning path would spuriously name
// every file the worker declared with a non-canonical prefix.
func TestMissionResult_VerifyOn_UndeclaredWarningUsesCanonicalComparison(t *testing.T) {
	home, repo, id := missionResultVerifyEnv(t)
	// Every diff path is declared, but c.txt is declared as `./c.txt`.
	// The warning loop must still treat it as declared.
	resultFile := writeVerifyResultFile(t, repo, id, []mission.FileChange{
		{Path: "a.txt", Added: 5, Removed: 0},
		{Path: "b.txt", Added: 3, Removed: 1},
		{Path: "./c.txt", Added: 3, Removed: 0},
	})

	cmd := exec.Command(ethosBinary,
		"mission", "result", id,
		"--file", resultFile,
		"--verify", "--base", "HEAD~1")
	cmd.Env = testGitEnv(home)
	cmd.Dir = repo
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	require.NoError(t, cmd.Run(),
		"verify must accept canonically equivalent paths: stderr=%s", errBuf.String())
	assert.Contains(t, outBuf.String(), "result:")
	stderr := errBuf.String()
	assert.NotContains(t, stderr, "warning",
		"`./c.txt` matches `c.txt` canonically; no warning expected")
	assert.NotContains(t, stderr, "c.txt",
		"the warning path must not name c.txt even obliquely")
}
