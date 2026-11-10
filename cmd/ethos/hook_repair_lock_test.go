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
