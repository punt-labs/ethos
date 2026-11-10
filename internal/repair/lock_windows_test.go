//go:build windows

package repair

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TryLock fails at once on a file another handle holds, and succeeds again
// once that handle is closed.
func TestTryLockFailsImmediatelyWhenHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	holder, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, TryLock(holder))

	second, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { second.Close() })
	require.Error(t, TryLock(second), "a held lock must fail without waiting")

	require.NoError(t, holder.Close())
	require.NoError(t, TryLock(second))
}
