//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func promptExportTempDir(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return real
}

func TestMissionExportFullRequiresActualHookCreatedPrompt(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the CLI regression requires a successfully built binary")
	se := &cliSubprocessEnv{home: promptExportTempDir(t), repo: promptExportTempDir(t)}
	t.Setenv("HOME", se.home)
	se.env = testGitEnv(se.home)
	init := exec.Command("git", "init", se.repo)
	init.Env = se.env
	out, err := init.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, "sentinel"), []byte("staged sentinel\n"), 0o600))
	add := exec.Command("git", "-C", se.repo, "add", "sentinel")
	add.Env = se.env
	out, err = add.CombinedOutput()
	require.NoError(t, err, "%s", out)
	p, err := layout.NewRepository(se.repo, se.repo, layout.RecordBlame)
	require.NoError(t, err)
	id := "m-2026-11-05-903"
	stamp := "2026-11-05T10:00:00Z"
	s := mission.NewRepositoryStore(p, filepath.Join(se.home, ".punt-labs", "ethos")).WithSessionID("hook-export-session")
	require.NoError(t, s.Create(&mission.Contract{MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp,
		Leader: "claude", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "rsc", PinnedAt: stamp},
		WriteSet: []string{"source.txt"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 1}, CurrentRound: 1}))
	se.env = append(se.env, "MISSION_ID="+id, "PARENT_DELEGATION_ID=", "PARENT_SESSION_ID=", "ETHOS_VERIFIER_ALLOWLIST=", "ETHOS_REPO_ROOT=", "ETHOS_QUIET_ADVICE=1")
	payload, err := json.Marshal(map[string]any{"tool_name": "Agent", "session_id": "hook-export-session",
		"tool_input": map[string]any{"subagent_type": "bwk", "prompt": "hook-created prompt reads " + se.home + "/notes"}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	dispatch := exec.CommandContext(ctx, ethosBinary, "hook", "pre-tool-use")
	dispatch.Dir = se.repo
	dispatch.Env = withForcedNotUnderClaudeCode(se.env)
	dispatch.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	dispatch.Stderr = &stderr
	out, err = dispatch.Output()
	require.NoError(t, err, "%s", stderr.String())
	var result hook.PreToolUseResult
	require.NoError(t, json.Unmarshal(out, &result))
	require.Empty(t, result.HookSpecificOutput.Decision(), result.HookSpecificOutput.PermissionDecisionReason)
	delegation := result.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"]
	require.NotEmpty(t, delegation)
	retained := filepath.Join(p.LocalRoot(), "trace", "missions", id, "delegations", delegation, "prompt.md")
	body, err := os.ReadFile(retained)
	require.NoError(t, err)
	record, err := mission.LoadDelegation(filepath.Join(p.RecordRoot(), "missions", id, "delegations", delegation, "record.yaml"))
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(body)), record.PromptHash, "the real hook supplied only prompt text, not a fabricated hash")
	assert.NoFileExists(t, filepath.Join(p.RecordRoot(), "missions", id, "delegations", delegation, "prompt.md"))
	before, err := os.ReadFile(filepath.Join(se.repo, ".git", "index"))
	require.NoError(t, err)
	_, exportErr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "complete")
	require.Equal(t, 0, exit, exportErr)
	assert.FileExists(t, filepath.Join(se.repo, "complete", id, "delegations", delegation, "prompt.md"))
	require.NoError(t, os.Remove(retained))
	stdout, exportErr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "incomplete")
	require.NotEqual(t, 0, exit, exportErr)
	assert.Contains(t, exportErr, "full export incomplete")
	assert.Contains(t, exportErr, "prompt unavailable")
	assert.Empty(t, stdout, "completeness is checked before any exported output")
	assert.NoDirExists(t, filepath.Join(se.repo, "incomplete"))
	after, err := os.ReadFile(filepath.Join(se.repo, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "both export attempts leave the actual index untouched")
}
