//go:build unix

package hook

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

// TestHandlePreToolUse_TierBDispatch_ExclusiveBlocks verifies the
// exclusive-side of the lock contract: a sibling holder of LOCK_EX
// on the canonical-local per-mission lock must block Tier B dispatch until
// it releases. Dispatch signals its lock boundary before the test observes
// that it remains blocked for 80ms; the old tracked .lock is not involved.
func TestHandlePreToolUse_TierBDispatch_ExclusiveBlocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	missionID := "m-2026-05-22-003"
	stageContract(t, home, missionID)

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	// Hold the established canonical-local lock, not the old tracked pathname.
	release, err := mission.AcquireMissionLockExclusive(repo, missionID)
	require.NoError(t, err)
	defer func() {
		if release != nil {
			release()
		}
	}()
	trying := make(chan struct{})
	old := dispatchTierBConfirmedOpen
	dispatchTierBConfirmedOpen = func() { close(trying) }
	t.Cleanup(func() { dispatchTierBConfirmedOpen = old })

	type result struct {
		decision string
		waited   time.Duration
		err      error
	}
	done := make(chan result, 1)
	go func() {
		payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-y"}`
		start := time.Now()
		var out bytes.Buffer
		if err := HandlePreToolUse(strings.NewReader(payload), &out); err != nil {
			done <- result{err: err}
			return
		}
		waited := time.Since(start)
		var r PreToolUseResult
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			done <- result{err: err}
			return
		}
		done <- result{decision: r.HookSpecificOutput.Decision(), waited: waited}
	}()

	select {
	case <-trying:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not reach mission lock boundary")
	}
	select {
	case <-done:
		t.Fatal("dispatch completed while exclusive lock was still held")
	case <-time.After(80 * time.Millisecond):
	}
	release()
	release = nil

	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.Equal(t, "", r.decision,
			"dispatch must allow after exclusive holder releases")
		assert.GreaterOrEqual(t, r.waited, 60*time.Millisecond,
			"dispatch wait must reflect the exclusive hold (got %v)", r.waited)
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not complete within 5s after exclusive release")
	}
}
