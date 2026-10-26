//go:build !windows

package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dispatch lock test holds a file lock with syscall.Flock, which Windows
// lacks; it lives apart from the tests that run everywhere.

// TestHandlePreToolUse_TierBDispatch_ExclusiveBlocks verifies the
// exclusive-side of the lock contract: a sibling holder of LOCK_EX
// on the per-mission .lock file must block the Tier B dispatch until
// it releases. The test holds LOCK_EX in a goroutine for 80ms, fires
// the dispatch in another goroutine, and asserts the dispatch's wait
// time reflects the hold.
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

	// Stage the per-mission dir + .lock so we can hold LOCK_EX on the
	// same path the dispatch will try to share-lock.
	dir := filepath.Join(repo, ".punt-labs", "ethos", "missions", missionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	lockPath := filepath.Join(dir, ".lock")
	excl, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(excl.Fd()), syscall.LOCK_EX))

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
		done <- result{decision: r.HookSpecificOutput.PermissionDecision, waited: waited}
	}()

	hold := 80 * time.Millisecond
	time.Sleep(hold)
	require.NoError(t, syscall.Flock(int(excl.Fd()), syscall.LOCK_UN))
	require.NoError(t, excl.Close())

	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.Equal(t, "allow", r.decision,
			"dispatch must allow after exclusive holder releases")
		assert.GreaterOrEqual(t, r.waited, 60*time.Millisecond,
			"dispatch wait must reflect the exclusive hold (got %v)", r.waited)
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not complete within 5s after exclusive release")
	}
}
