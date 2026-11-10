//go:build windows

package identity

import (
	"fmt"
	"os"
)

func flock(_ *os.File) error {
	return fmt.Errorf("file locking not supported on Windows")
}

func funlock(_ *os.File) error {
	return nil
}

// tryFlock succeeds without locking: Windows is not a shipped target and the
// repair engine is a no-op there, so the steps must not fail on a lock that
// cannot exist.
func tryFlock(_ *os.File) error {
	return nil
}
