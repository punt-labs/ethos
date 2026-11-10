//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// The session-start pass writes the repair log and retires session state, both
// of which assume one writer: it runs under the engine's global lock and leaves
// the work alone while another repair holds it.
func TestRepairSessionsTakesTheGlobalLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".punt-labs", "ethos")
	current := filepath.Join(global, "sessions", "current")
	require.NoError(t, os.MkdirAll(current, 0o700))
	dead := filepath.Join(current, "2147483646")
	require.NoError(t, os.WriteFile(dead, []byte("s\n"), 0o600))

	repair := repairSessions(resolve.Located{})

	release, ok, err := autorepair.TryGlobalLock(global, 0)
	require.NoError(t, err)
	require.True(t, ok)
	n, err := repair(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, n, "a held lock leaves the work to its holder")
	assert.FileExists(t, dead)
	release()

	n, err = repair(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.NoFileExists(t, dead)

	// The pass released the lock on return.
	again, ok, err := autorepair.TryGlobalLock(global, 50*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, ok)
	again()
}

// While another process holds the repair lock, a session start purges nothing,
// prints nothing on stderr, exits 0, and does not wait for the lock: the
// session-start pass under the lock owns current/ purging.
func TestCLI_SessionStartWithRepairLockHeldPurgesNothingAndDoesNotWait(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	// The first start pays the cold-cache cost; the timed one measures the wait.
	runHookIn(t, se, se.repo, "session-start", `{"session_id":"warm-s0","hook_event_name":"SessionStart"}`)
	global := filepath.Join(se.home, ".punt-labs", "ethos")
	current := filepath.Join(global, "sessions", "current")
	require.NoError(t, os.MkdirAll(current, 0o700))
	dead := filepath.Join(current, "2147483646")
	require.NoError(t, os.WriteFile(dead, []byte("s\n"), 0o600))

	release, ok, err := autorepair.TryGlobalLock(global, 0)
	require.NoError(t, err)
	require.True(t, ok)
	defer release()

	start := time.Now()
	_, stderr, code := runHookIn(t, se, se.repo, "session-start", `{"session_id":"lock-s1","hook_event_name":"SessionStart"}`)
	elapsed := time.Since(start)

	assert.Equal(t, 0, code)
	assert.FileExists(t, dead, "nothing is purged while another process repairs")
	assert.NotContains(t, stderr, "stale PID", "no purge line on stderr")
	assert.Less(t, elapsed, 500*time.Millisecond, "the pass does not wait on the lock")
}
