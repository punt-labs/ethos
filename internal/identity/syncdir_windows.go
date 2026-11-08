//go:build windows

package identity

// As in internal/mission, Go provides no portable directory-fsync equivalent
// on Windows. This no-op keeps cross-compilation working, not a promise of
// directory-entry durability or native Windows support. Identity locking is
// already unsupported on Windows (flock_windows.go).
func syncDirectory(string) error { return nil }
