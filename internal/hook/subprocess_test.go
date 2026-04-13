//go:build linux || darwin

package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// ethosBinary is the path to the compiled ethos binary, built once
// per test run by TestMain. Empty if the build failed.
var ethosBinary string

// moduleRoot returns the repo root (two levels above internal/hook/).
func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "..", "..")
}

// subprocessEnv holds the filesystem layout and env vars for a
// subprocess test.
type subprocessEnv struct {
	home string   // fake HOME
	repo string   // fake repo root
	env  []string // child process environment
}

// setupSubprocessEnv creates the minimal filesystem the ethos binary
// needs: a global identity store, a repo with config, and a real git
// init (so FindRepoRoot stops at the fake repo, not the real one).
func setupSubprocessEnv(t *testing.T) *subprocessEnv {
	t.Helper()

	home := t.TempDir()
	repo := t.TempDir()

	// Global identity store.
	globalEthos := filepath.Join(home, ".punt-labs", "ethos")
	globalIDs := filepath.Join(globalEthos, "identities")
	require.NoError(t, os.MkdirAll(globalIDs, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(globalEthos, "sessions"), 0o700))

	idData, err := yaml.Marshal(map[string]interface{}{
		"name":   "Test Agent",
		"handle": "test-agent",
		"kind":   "agent",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(globalIDs, "test-agent.yaml"), idData, 0o644))

	// Repo: git init so FindRepoRoot stops here.
	repoEthos := filepath.Join(repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(filepath.Join(repoEthos, "identities"), 0o755))

	gitEnv := []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	gitInit := exec.Command("git", "init", repo)
	gitInit.Env = gitEnv
	initOut, initErr := gitInit.CombinedOutput()
	require.NoError(t, initErr, "git init failed: %s", initOut)

	// Repo config.
	cfgData, err := yaml.Marshal(map[string]string{"agent": "test-agent"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), cfgData, 0o644))

	env := []string{
		"HOME=" + home,
		"USER=testuser",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}

	return &subprocessEnv{home: home, repo: repo, env: env}
}

// runHookSubprocess spawns the ethos binary with "hook <event>",
// writes payload to stdin via an inherited pipe fd, and waits for the
// process to exit within 5 seconds. Both pipe ends are deferred-closed
// so they are cleaned up on any early return (e.g. assertion failure).
// The child uses readWithTimeout (single f.Read with a timer), so it
// does not require EOF to proceed -- data in the pipe buffer suffices.
func runHookSubprocess(t *testing.T, se *subprocessEnv, event, payload string) (stdout, stderr string, err error) {
	t.Helper()

	rFd, wFd, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	defer rFd.Close()
	defer wFd.Close()

	_, writeErr := wFd.Write([]byte(payload))
	require.NoError(t, writeErr)

	cmd := exec.Command(ethosBinary, "hook", event)
	cmd.Stdin = rFd
	cmd.Dir = se.repo
	cmd.Env = se.env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	require.NoError(t, cmd.Start())

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case waitErr := <-done:
		return outBuf.String(), errBuf.String(), waitErr
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("hook %s hung -- did not exit within 5 seconds\nstderr: %s", event, errBuf.String())
		return "", "", fmt.Errorf("timeout")
	}
}

// sessionsDir returns the sessions directory under the fake HOME.
func sessionsDir(se *subprocessEnv) string {
	return filepath.Join(se.home, ".punt-labs", "ethos", "sessions")
}

func TestSubprocess_SessionStart(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupSubprocessEnv(t)
	sid := "test-sub-ss-001"
	payload := fmt.Sprintf(`{"session_id":%q}`, sid)

	stdout, stderr, err := runHookSubprocess(t, se, "session-start", payload)
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	require.NoError(t, err, "session-start exited non-zero: %s", stderr)

	// Session roster file created.
	rosterPath := filepath.Join(sessionsDir(se), sid+".yaml")
	_, statErr := os.Stat(rosterPath)
	assert.NoError(t, statErr, "session roster file should exist at %s", rosterPath)

	// Current PID file created.
	currentDir := filepath.Join(sessionsDir(se), "current")
	entries, dirErr := os.ReadDir(currentDir)
	if dirErr == nil {
		assert.Greater(t, len(entries), 0, "current/ should have at least one PID file")
	}

	// Stdout is valid JSON with hookSpecificOutput.
	assert.NotEmpty(t, stdout, "stdout should contain JSON output")
	if stdout != "" {
		var result map[string]interface{}
		assert.NoError(t, json.Unmarshal([]byte(stdout), &result), "stdout should be valid JSON")
		hso, ok := result["hookSpecificOutput"]
		assert.True(t, ok, "JSON should contain hookSpecificOutput")
		if m, ok := hso.(map[string]interface{}); ok {
			assert.Equal(t, "SessionStart", m["hookEventName"])
		}
	}
}

func TestSubprocess_PreCompact(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupSubprocessEnv(t)
	sid := "test-sub-pc-001"

	// PreCompact reads an existing roster. Create one first.
	startPayload := fmt.Sprintf(`{"session_id":%q}`, sid)
	_, _, startErr := runHookSubprocess(t, se, "session-start", startPayload)
	require.NoError(t, startErr, "session-start setup failed")

	payload := fmt.Sprintf(`{"session_id":%q}`, sid)
	stdout, stderr, err := runHookSubprocess(t, se, "pre-compact", payload)
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	require.NoError(t, err, "pre-compact exited non-zero: %s", stderr)

	// PreCompact emits the persona block as plain text when it finds
	// the agent in the roster. The primary assertion is exit 0 without
	// hanging. The output may be empty if the roster's primary agent
	// PID doesn't match the child's parent PID.
}

func TestSubprocess_SubagentStart(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupSubprocessEnv(t)
	sid := "test-sub-sa-001"

	// Create parent session.
	startPayload := fmt.Sprintf(`{"session_id":%q}`, sid)
	_, _, startErr := runHookSubprocess(t, se, "session-start", startPayload)
	require.NoError(t, startErr, "session-start setup failed")

	payload := fmt.Sprintf(`{"session_id":%q,"agent_id":"sub-001","agent_type":"code-reviewer"}`, sid)
	stdout, stderr, err := runHookSubprocess(t, se, "subagent-start", payload)
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	require.NoError(t, err, "subagent-start exited non-zero: %s", stderr)

	// Participant added to roster.
	rosterPath := filepath.Join(sessionsDir(se), sid+".yaml")
	data, readErr := os.ReadFile(rosterPath)
	require.NoError(t, readErr, "roster should exist")

	var roster map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &roster))

	participants, ok := roster["participants"].([]interface{})
	require.True(t, ok, "roster should have participants")

	found := false
	for _, p := range participants {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if pm["agent_id"] == "sub-001" {
			found = true
			assert.Equal(t, "code-reviewer", pm["agent_type"])
			break
		}
	}
	assert.True(t, found, "sub-001 should appear in the session roster")
}

func TestSubprocess_SubagentStop(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupSubprocessEnv(t)
	sid := "test-sub-stop-001"

	// Create session and add subagent.
	startPayload := fmt.Sprintf(`{"session_id":%q}`, sid)
	_, _, err := runHookSubprocess(t, se, "session-start", startPayload)
	require.NoError(t, err)

	joinPayload := fmt.Sprintf(`{"session_id":%q,"agent_id":"sub-stop-001","agent_type":"reviewer"}`, sid)
	_, _, err = runHookSubprocess(t, se, "subagent-start", joinPayload)
	require.NoError(t, err)

	// Stop the subagent.
	stopPayload := fmt.Sprintf(`{"session_id":%q,"agent_id":"sub-stop-001"}`, sid)
	stdout, stderr, err := runHookSubprocess(t, se, "subagent-stop", stopPayload)
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	require.NoError(t, err, "subagent-stop exited non-zero: %s", stderr)

	// Participant removed from roster.
	rosterPath := filepath.Join(sessionsDir(se), sid+".yaml")
	data, readErr := os.ReadFile(rosterPath)
	require.NoError(t, readErr)

	var roster map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &roster))

	participants, ok := roster["participants"].([]interface{})
	require.True(t, ok)

	for _, p := range participants {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		assert.NotEqual(t, "sub-stop-001", pm["agent_id"],
			"sub-stop-001 should have been removed from roster")
	}
}

func TestSubprocess_SessionEnd(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupSubprocessEnv(t)
	sid := "test-sub-se-001"

	// Create session.
	startPayload := fmt.Sprintf(`{"session_id":%q}`, sid)
	_, _, err := runHookSubprocess(t, se, "session-start", startPayload)
	require.NoError(t, err)

	rosterPath := filepath.Join(sessionsDir(se), sid+".yaml")
	_, statErr := os.Stat(rosterPath)
	require.NoError(t, statErr, "roster should exist before session-end")

	// End the session.
	endPayload := fmt.Sprintf(`{"session_id":%q}`, sid)
	stdout, stderr, err := runHookSubprocess(t, se, "session-end", endPayload)
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	require.NoError(t, err, "session-end exited non-zero: %s", stderr)

	// Roster deleted.
	_, statErr = os.Stat(rosterPath)
	assert.True(t, os.IsNotExist(statErr), "session roster should be deleted after session-end")
}

// TestSubprocess_OpenPipe verifies that the hook process exits within
// the timeout when the parent keeps the pipe write end open. On Linux,
// inherited pipe fds don't support SetReadDeadline; readWithTimeout
// uses a single f.Read in a goroutine racing against a 1-second timer.
// With data in the pipe, f.Read returns it immediately even without
// EOF. The hook must exit cleanly (exit 0), not hang.
func TestSubprocess_OpenPipe(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupSubprocessEnv(t)

	rFd, wFd, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	defer rFd.Close()
	defer wFd.Close()

	// Write data but keep write end open.
	_, err := wFd.Write([]byte(`{"session_id":"open-pipe-test"}`))
	require.NoError(t, err)

	cmd := exec.Command(ethosBinary, "hook", "session-start")
	cmd.Stdin = rFd
	cmd.Dir = se.repo
	cmd.Env = se.env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	require.NoError(t, cmd.Start())

	// Do NOT close wFd — this is the open-pipe scenario.

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case waitErr := <-done:
		require.NoError(t, waitErr, "hook should exit 0 even with open pipe")
		t.Logf("stdout: %s", outBuf.String())
		t.Logf("stderr: %s", errBuf.String())
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("hook hung with open pipe -- did not exit within 5 seconds")
	}
}

// fakeClaudeJS is the Node.js script that reproduces Claude Code's exact
// hook spawn path: spawn(command, [], { shell: true }). This creates the
// /bin/sh -c intermediate that makes /dev/stdin inaccessible on Linux.
const fakeClaudeJS = `
const { spawn } = require('child_process');
const cmd = process.argv[2];
const payload = process.argv[3] || '{}';
const child = spawn(cmd, [], {
  shell: true,
  stdio: ['pipe', 'pipe', 'pipe'],
  env: Object.assign({}, process.env),
});
child.stdin.write(payload + '\n', 'utf8');
child.stdin.end();
let out = '', err = '';
child.stdout.on('data', (d) => { out += d; });
child.stderr.on('data', (d) => { err += d; });
const timer = setTimeout(() => {
  process.stderr.write('TIMEOUT\\n');
  child.kill('SIGKILL');
  process.exit(1);
}, 5000);
child.on('close', (code) => {
  clearTimeout(timer);
  if (out) process.stdout.write(out);
  if (err) process.stderr.write(err);
  process.exit(code || 0);
});
`

// TestShellScript_SessionStart reproduces Claude Code's exact hook
// execution path using Node.js spawn(shell: true). Claude Code runs
// hooks as /bin/sh -c "<command>", which makes /dev/stdin inaccessible
// inside the hook subprocess on Linux.
//
// This is the regression gate for DES-029: if someone reverts the
// shell script to use "< /dev/stdin", this test fails on Linux because
// /dev/stdin resolves to "No such device or address" inside the
// /bin/sh -c intermediate.
func TestShellScript_SessionStart(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found — required to simulate Claude Code spawn")
	}

	se := setupSubprocessEnv(t)
	sid := "test-shell-script-001"

	hookScript := filepath.Join(moduleRoot(t), "hooks", "session-start.sh")
	_, err := os.Stat(hookScript)
	require.NoError(t, err, "session-start.sh not found at %s", hookScript)

	payload := fmt.Sprintf(`{"session_id":%q}`, sid)

	// Write the fake Claude JS to a temp file.
	jsFile := filepath.Join(t.TempDir(), "fake_claude.js")
	require.NoError(t, os.WriteFile(jsFile, []byte(fakeClaudeJS), 0o644))

	// The hook command must cd to the repo and run the hook script,
	// matching how Claude Code invokes: spawn("cd /repo && bash hook.sh", { shell: true })
	hookCmd := fmt.Sprintf("cd %q && bash %q", se.repo, hookScript)

	cmd := exec.Command("node", jsFile, hookCmd, payload)
	// Prepend the ethos binary dir to PATH rather than appending a duplicate.
	binDir := filepath.Dir(ethosBinary)
	env := make([]string, 0, len(se.env))
	for _, e := range se.env {
		if len(e) > 5 && e[:5] == "PATH=" {
			e = "PATH=" + binDir + ":" + e[5:]
		}
		env = append(env, e)
	}
	cmd.Env = env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()
	t.Logf("stdout: %s", outBuf.String())
	t.Logf("stderr: %s", errBuf.String())
	require.NoError(t, err, "hook exited non-zero: %s", errBuf.String())

	// The real assertion: session roster file must exist.
	rosterPath := filepath.Join(sessionsDir(se), sid+".yaml")
	_, statErr := os.Stat(rosterPath)
	assert.NoError(t, statErr,
		"session roster should exist — shell script must read stdin via "+
			"'read -t 1' and forward via 'printf |', not '< /dev/stdin'")

	// Stdout should contain the persona block JSON.
	assert.NotEmpty(t, outBuf.String(), "stdout should contain hookSpecificOutput JSON")
}

// seedVerifierMission writes a minimal open mission contract directly
// to the subprocess env's mission store, naming the given agent handle
// as the evaluator. The mission has an empty pinned hash so the
// subagent-start hook's Phase 3.3 drift gate accepts it as legacy and
// falls through to Phase 3.5's context-isolation block path.
//
// The empty hash is the same trick TestSubagentStart_VerifierGateLegacyMissionAllowsSpawn
// uses in-process: pre-3.3 contracts that predate the gate are let
// through with a stderr warning so the upgrade path is clean, and the
// warning is irrelevant to this test's stdout assertions.
//
// Returns the mission ID so the caller can assert it appears in the
// isolation block.
func seedVerifierMission(t *testing.T, se *subprocessEnv, evaluator string) string {
	t.Helper()
	missionsDir := filepath.Join(se.home, ".punt-labs", "ethos", "missions")
	require.NoError(t, os.MkdirAll(missionsDir, 0o700))

	// Build a minimal valid Contract value and marshal it directly. The
	// mission package's writeContract is unexported, so the test
	// re-implements the rename-safe write and skips the create lock —
	// this fixture path never races with a real CLI invocation.
	missionID := "m-2026-04-08-900"
	c := mission.Contract{
		MissionID: missionID,
		Status:    mission.StatusOpen,
		CreatedAt: "2026-04-08T00:00:00Z",
		UpdatedAt: "2026-04-08T00:00:00Z",
		Leader:    "claude",
		Worker:    "bwk",
		Evaluator: mission.Evaluator{
			Handle:   evaluator,
			PinnedAt: "2026-04-08T00:00:00Z",
			// Empty Hash = legacy mission, skipped by the hash gate.
		},
		Inputs: mission.Inputs{
			Ticket: "ethos-07m.9",
		},
		WriteSet: []string{
			"internal/mission/store.go",
			"internal/hook/subagent_start.go",
		},
		SuccessCriteria: []string{
			"isolation block renders under H2",
			"allowlist is split into repo-relative and absolute",
		},
		Budget: mission.Budget{
			Rounds:              3,
			ReflectionAfterEach: true,
		},
		CurrentRound: 1,
	}
	data, err := yaml.Marshal(&c)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(missionsDir, missionID+".yaml"), data, 0o600))
	return missionID
}

// TestSubprocess_VerifierIsolationBlock exercises the full Phase 3.5
// isolation path through the real ethos binary. It seeds a mission
// naming test-agent as the evaluator, spawns subagent-start with
// agent_type=test-agent, and asserts the JSON additionalContext
// carries the isolation block markers and excludes the persona block
// markers. A second spawn with agent_type set to a different handle
// asserts the opposite: non-verifier spawns are untouched.
//
// The test is the end-to-end counterpart to the in-process
// TestSubagentStart_VerifierIsolationBlockShape unit test — a wiring
// bug that only surfaces at the binary level (bad env, bad flag
// plumbing, missing import) would be caught here.
func TestSubprocess_VerifierIsolationBlock(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupSubprocessEnv(t)
	sid := "test-sub-vi-001"

	// Parent session is required for the subagent-start join path to
	// have a target.
	startPayload := fmt.Sprintf(`{"session_id":%q}`, sid)
	_, _, startErr := runHookSubprocess(t, se, "session-start", startPayload)
	require.NoError(t, startErr, "session-start setup failed")

	// Seed a mission naming test-agent as the evaluator. The default
	// subprocessEnv already created the test-agent identity in the
	// global store.
	missionID := seedVerifierMission(t, se, "test-agent")

	// Spawn as the evaluator handle — the isolation block path MUST
	// fire because an open mission names test-agent as evaluator.
	verifierPayload := fmt.Sprintf(
		`{"session_id":%q,"agent_id":"sub-vi-001","agent_type":"test-agent"}`, sid)
	stdout, stderr, err := runHookSubprocess(t, se, "subagent-start", verifierPayload)
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)
	require.NoError(t, err, "subagent-start exited non-zero: %s", stderr)
	require.NotEmpty(t, stdout, "verifier spawn must emit a JSON envelope")

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	assert.Equal(t, "SubagentStart", result.HookSpecificOutput.HookEventName)
	ctx := result.HookSpecificOutput.AdditionalContext
	require.NotEmpty(t, ctx, "verifier spawn must carry an additionalContext block")

	// Isolation block markers — the Phase 3.5 round 2 header layout
	// uses H2 for the root and H3 for sub-sections.
	assert.Contains(t, ctx, "## Verifier context",
		"isolation block must begin with an H2 header")
	assert.Contains(t, ctx, missionID,
		"isolation block must name the mission in the header")
	assert.Contains(t, ctx, `frozen verifier "test-agent"`,
		"isolation block must name the verifier with the handle")
	assert.Contains(t, ctx, "### Mission contract",
		"isolation block must include the mission contract sub-section")
	assert.Contains(t, ctx, "### Verification criteria",
		"isolation block must include the verification criteria sub-section")
	assert.Contains(t, ctx, "### File allowlist",
		"isolation block must include the file allowlist sub-section")
	assert.Contains(t, ctx, "internal/mission/store.go",
		"allowlist must include write_set entries")
	assert.Contains(t, ctx, "Repo-relative paths",
		"allowlist must label the repo-relative section")
	assert.Contains(t, ctx, "Absolute paths",
		"allowlist must label the absolute section")
	assert.Contains(t, ctx, "MUST NOT read",
		"isolation block must carry the explicit MUST NOT directive")

	// Persona block markers must be absent — a verifier spawn replaces
	// the persona/extension injection with the isolation block, not
	// layers it on top.
	assert.NotContains(t, ctx, "## Personality",
		"Phase 3.5: persona block must not appear on verifier spawns")
	assert.NotContains(t, ctx, "## Writing Style",
		"Phase 3.5: writing-style block must not appear on verifier spawns")
	assert.NotContains(t, ctx, "You report to",
		"Phase 3.5: parent-reports-to line must not appear on verifier spawns")

	// Flip side: a spawn whose agent_type is NOT the evaluator must
	// fall through to the normal persona path. Seed a second identity
	// into the global store so the normal path has content to render.
	otherID, err := yaml.Marshal(map[string]interface{}{
		"name":   "Worker Agent",
		"handle": "worker-agent",
		"kind":   "agent",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(se.home, ".punt-labs", "ethos", "identities", "worker-agent.yaml"),
		otherID, 0o644))

	nonVerifierPayload := fmt.Sprintf(
		`{"session_id":%q,"agent_id":"sub-vi-002","agent_type":"worker-agent"}`, sid)
	stdout2, stderr2, err := runHookSubprocess(t, se, "subagent-start", nonVerifierPayload)
	t.Logf("non-verifier stdout: %s", stdout2)
	t.Logf("non-verifier stderr: %s", stderr2)
	require.NoError(t, err, "non-verifier subagent-start exited non-zero: %s", stderr2)

	// The non-verifier path may return either an empty stdout (no
	// persona content) or a persona block. In either case, the
	// isolation-block markers MUST be absent.
	if stdout2 != "" {
		var result2 SubagentStartResult
		require.NoError(t, json.Unmarshal([]byte(stdout2), &result2))
		ctx2 := result2.HookSpecificOutput.AdditionalContext
		assert.NotContains(t, ctx2, "## Verifier context",
			"non-verifier spawn must not receive the isolation block")
		assert.NotContains(t, ctx2, "### Mission contract",
			"non-verifier spawn must not receive the contract sub-section")
	}
}

func TestSubprocess_AuditLog(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	se := setupSubprocessEnv(t)
	sid := "test-audit-001"
	payload := `{"session_id":"test-audit-001","tool_name":"Bash","tool_input":{"command":"git status"}}`

	stdout, stderr, err := runHookSubprocess(t, se, "audit-log", payload)
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	require.NoError(t, err, "audit-log exited non-zero: %s", stderr)

	// Audit log file created.
	auditPath := filepath.Join(sessionsDir(se), sid+".audit.jsonl")
	data, readErr := os.ReadFile(auditPath)
	require.NoError(t, readErr, "audit log file should exist at %s", auditPath)

	// File contains one JSONL line with the tool name.
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	assert.Len(t, lines, 1, "audit log should contain exactly one line")

	var entry map[string]interface{}
	require.NoError(t, json.Unmarshal(lines[0], &entry), "audit line should be valid JSON")
	assert.Equal(t, "Bash", entry["tool"])
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ethos-subprocess-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir for binary: %v\n", err)
		os.Exit(1)
	}

	bin := filepath.Join(dir, "ethos")
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getting working directory: %v\n", err)
		os.Exit(1)
	}
	root := filepath.Join(wd, "..", "..")

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/ethos")
	cmd.Dir = root
	out, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		fmt.Fprintf(os.Stderr, "go build failed: %v\n%s\n", buildErr, out)
		// Leave ethosBinary empty; subprocess tests skip, unit tests still run.
	} else {
		ethosBinary = bin
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
