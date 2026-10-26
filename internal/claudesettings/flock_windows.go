//go:build windows

package claudesettings

import "os"

func openLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
}

// flock is a no-op on Windows, matching internal/claudemd.
func flock(_ *os.File) error { return nil }

func funlock(_ *os.File) error { return nil }
