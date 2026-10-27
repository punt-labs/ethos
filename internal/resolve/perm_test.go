package resolve

import (
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not POSIX permissions here")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
}

// chmodUnreadable makes p mode 000 for the test, restoring mode after.
func chmodUnreadable(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	skipIfRoot(t)
	require.NoError(t, os.Chmod(p, 0o000))
	t.Cleanup(func() { _ = os.Chmod(p, mode) })
}
