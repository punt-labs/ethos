package hook

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

const dispatchPromptMission = "m-2026-11-05-902"

func dispatchPromptRealPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return real
}

func dispatchPromptGit(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	out, err := gitenv.Output(repo, "git", args...)
	require.NoError(t, err, "git %v: %s", args, out)
	return out
}

func dispatchPromptFixture(t *testing.T, mode layout.RecordMode) layout.Policy {
	t.Helper()
	t.Setenv("HOME", dispatchPromptRealPath(t, t.TempDir()))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("ETHOS_REPO_ROOT", "")
	t.Setenv("MISSION_ID", dispatchPromptMission)
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("PARENT_SESSION_ID", "")
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("ETHOS_QUIET_ADVICE", "1")
	store := dispatchPromptRealPath(t, stageRepoRoot(t))
	require.NoError(t, os.WriteFile(filepath.Join(store, ".gitignore"), []byte("/.punt-labs/local/\n"), 0o600))
	dispatchPromptGit(t, store, "add", ".gitignore")
	dispatchPromptGit(t, store, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "base")
	checkout := dispatchPromptRealPath(t, t.TempDir())
	dispatchPromptGit(t, store, "worktree", "add", "--detach", checkout, "HEAD")
	p, err := layout.NewRepository(store, checkout, mode)
	require.NoError(t, err)
	s := mission.NewRepositoryStore(p, filepath.Join(os.Getenv("HOME"), ".punt-labs", "ethos"))
	stamp := "2026-11-05T10:00:00Z"
	require.NoError(t, s.Create(&mission.Contract{MissionID: dispatchPromptMission, Status: mission.StatusOpen,
		CreatedAt: stamp, UpdatedAt: stamp, Leader: "claude", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "rsc", PinnedAt: stamp},
		WriteSet: []string{"source.txt"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 1}, CurrentRound: 1}))
	record := "blame"
	if mode == layout.RecordFull {
		record = "full"
	}
	require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte("record: "+record+"\n"), 0o600))
	require.NoError(t, os.Chdir(checkout)) // stageRepoRoot's cleanup restores the original cwd.
	return p
}

func dispatchPrompt(t *testing.T, prompt string) PreToolUseResult {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"tool_name": "Agent", "session_id": "prompt-dispatch-session",
		"tool_input": map[string]any{"subagent_type": "bwk", "prompt": prompt}})
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(bytes.NewReader(payload), &out))
	var result PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	return result
}

func TestDispatchPromptCapturedFullPublishesAndBlameStaysPrivate(t *testing.T) {
	for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		t.Run(fmt.Sprintf("mode-%d", mode), func(t *testing.T) {
			p := dispatchPromptFixture(t, mode)
			checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
			index := string(bytes.TrimSpace(dispatchPromptGit(t, checkout, "rev-parse", "--path-format=absolute", "--git-path", "index")))
			before, err := os.ReadFile(index)
			require.NoError(t, err)
			result := dispatchPrompt(t, "read "+os.Getenv("HOME")+"/notes for private@example.invalid")
			require.Empty(t, result.HookSpecificOutput.Decision(), result.HookSpecificOutput.PermissionDecisionReason)
			id := result.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"]
			require.NotEmpty(t, id)
			local := filepath.Join(p.LocalRoot(), "trace", "missions", dispatchPromptMission, "delegations", id, "prompt.md")
			body, err := os.ReadFile(local)
			require.NoError(t, err)
			assert.NotContains(t, string(body), os.Getenv("HOME"))
			assert.NotContains(t, string(body), "private@example.invalid")
			record, err := mission.LoadDelegation(filepath.Join(p.RecordRoot(), "missions", dispatchPromptMission, "delegations", id, "record.yaml"))
			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(body)), record.PromptHash, "the production hook supplies no hash")
			after, err := os.ReadFile(index)
			require.NoError(t, err)
			assert.Equal(t, before, after, "recording must not stage")
			published := filepath.Join(p.CheckoutRecordRoot(), "missions", dispatchPromptMission, "delegations", id, "prompt.md")
			if mode == layout.RecordFull {
				copy, err := os.ReadFile(published)
				require.NoError(t, err)
				assert.Equal(t, body, copy)
				rel, err := filepath.Rel(checkout, published)
				require.NoError(t, err)
				dispatchPromptGit(t, checkout, "add", "--", filepath.ToSlash(rel))
				assert.Equal(t, body, dispatchPromptGit(t, checkout, "show", ":"+filepath.ToSlash(rel)), "ordinary git add, without force or ignore exceptions")
			} else {
				assert.NoFileExists(t, published)
				full, err := layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), checkout, layout.RecordFull)
				require.NoError(t, err)
				require.NoError(t, mission.RetryPromptPublications(full))
				assert.NoFileExists(t, published, "full retry cannot promote this hook's private prompt")
			}
			_, err = os.Lstat(filepath.Join(checkout, ".punt-labs", "local", "ethos"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestDispatchPromptFullFailureIsVisibleAndRetryableAfterModeChange(t *testing.T) {
	p := dispatchPromptFixture(t, layout.RecordFull)
	blocker := filepath.Join(p.CheckoutRecordRoot(), "missions")
	require.NoError(t, os.MkdirAll(filepath.Dir(blocker), 0o700))
	require.NoError(t, os.WriteFile(blocker, []byte("existing checkout file"), 0o600))
	result := dispatchPrompt(t, "captured full prompt")
	require.Equal(t, "deny", result.HookSpecificOutput.Decision())
	assert.Contains(t, result.HookSpecificOutput.PermissionDecisionReason, "pending")
	dir := filepath.Join(p.LocalRoot(), "trace", "missions", dispatchPromptMission, "delegations")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	id := entries[0].Name()
	assert.FileExists(t, filepath.Join(dir, id, "prompt.md"))
	assert.FileExists(t, filepath.Join(dir, id, "prompt-consent.yaml"))
	store := filepath.Dir(filepath.Dir(p.RecordRoot()))
	require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte("record: blame\n"), 0o600))
	require.NoError(t, os.Remove(blocker))
	blame, err := layout.NewRepository(store, filepath.Dir(filepath.Dir(p.CheckoutRecordRoot())), layout.RecordBlame)
	require.NoError(t, err)
	require.NoError(t, mission.RetryPromptPublications(blame))
	published := filepath.Join(p.CheckoutRecordRoot(), "missions", dispatchPromptMission, "delegations", id, "prompt.md")
	body, err := os.ReadFile(published)
	require.NoError(t, err)
	assert.Equal(t, "captured full prompt", string(body))
}

func TestDispatchPromptFailureReservesIdentityForDifferentNextPrompt(t *testing.T) {
	for _, receiptOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("receipt-only-%t", receiptOnly), func(t *testing.T) {
			p := dispatchPromptFixture(t, layout.RecordFull)
			firstPrompt := "first immutable prompt"
			local := filepath.Join(p.LocalRoot(), "trace", "missions", dispatchPromptMission, "delegations")
			blocker := filepath.Join(p.CheckoutRecordRoot(), "missions")
			if receiptOnly {
				// Model interruption after receipt installation, before payload.
				// Predict the next ID without consuming it, then prevent payload
				// staging so the real hook fails with only that receipt retained.
				id, release, err := mission.NewID(mission.NamespaceDelegations, time.Now())
				require.NoError(t, err)
				release(false)
				dir := filepath.Join(local, id)
				require.NoError(t, os.MkdirAll(dir, 0o700))
				consent := fmt.Sprintf("version: 1\nmission: %s\ndelegation: %s\nsha256: %x\npublish: true\n", dispatchPromptMission, id, sha256.Sum256([]byte(firstPrompt)))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt-consent.yaml"), []byte(consent), 0o600))
				blocker = p.TempDir()
				require.NoError(t, os.Remove(blocker)) // preceding mission writes cleaned their staging.
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(blocker), 0o700))
			require.NoError(t, os.WriteFile(blocker, []byte("existing blocker"), 0o600))
			first := dispatchPrompt(t, firstPrompt)
			require.Equal(t, "deny", first.HookSpecificOutput.Decision())
			assert.Contains(t, first.HookSpecificOutput.PermissionDecisionReason, "prompt")
			assert.Empty(t, first.HookSpecificOutput.AdditionalEnv, "reserved does not mean admitted")
			entries, err := os.ReadDir(local)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			reservedID := entries[0].Name()
			reservedDir := filepath.Join(local, reservedID)
			consent, err := os.ReadFile(filepath.Join(reservedDir, "prompt-consent.yaml"))
			require.NoError(t, err)
			assert.NoFileExists(t, filepath.Join(p.RecordRoot(), "missions", dispatchPromptMission, "delegations", reservedID, "record.yaml"))
			if receiptOnly {
				assert.NoFileExists(t, filepath.Join(reservedDir, "prompt.md"))
			} else {
				body, err := os.ReadFile(filepath.Join(reservedDir, "prompt.md"))
				require.NoError(t, err)
				assert.Equal(t, firstPrompt, string(body))
			}
			require.NoError(t, os.Remove(blocker))
			secondPrompt := "a different prompt after repair"
			second := dispatchPrompt(t, secondPrompt)
			require.Empty(t, second.HookSpecificOutput.Decision(), second.HookSpecificOutput.PermissionDecisionReason)
			id := second.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"]
			require.NotEmpty(t, id)
			require.NotEqual(t, reservedID, id, "a retained receipt or payload permanently occupies its ID")
			record, err := mission.LoadDelegation(filepath.Join(p.RecordRoot(), "missions", dispatchPromptMission, "delegations", id, "record.yaml"))
			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(secondPrompt))), record.PromptHash)
			for _, path := range []string{filepath.Join(local, id, "prompt.md"), filepath.Join(p.CheckoutRecordRoot(), "missions", dispatchPromptMission, "delegations", id, "prompt.md")} {
				body, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, secondPrompt, string(body))
			}
			after, err := os.ReadFile(filepath.Join(reservedDir, "prompt-consent.yaml"))
			require.NoError(t, err)
			assert.Equal(t, consent, after, "do not erase or rewrite the failed attempt's immutable evidence")
			if receiptOnly {
				assert.NoFileExists(t, filepath.Join(reservedDir, "prompt.md"))
			} else {
				body, err := os.ReadFile(filepath.Join(reservedDir, "prompt.md"))
				require.NoError(t, err)
				assert.Equal(t, firstPrompt, string(body))
			}
		})
	}
}
