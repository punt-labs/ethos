//go:build !windows

package autorepair

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryGlobalLockExcludesASecondHolder(t *testing.T) {
	global := t.TempDir()

	release, ok, err := TryGlobalLock(global, 0)
	require.NoError(t, err)
	require.True(t, ok)

	_, again, err := TryGlobalLock(global, 50*time.Millisecond)
	require.NoError(t, err)
	assert.False(t, again, "a held lock is busy, not an error")

	release()
	release2, ok, err := TryGlobalLock(global, 0)
	require.NoError(t, err)
	assert.True(t, ok, "released lock is free")
	release2()
}
