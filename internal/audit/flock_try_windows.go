//go:build windows

package audit

// TryLock runs fn without file locking on Windows, like WithLock. Windows is
// not a shipped target (DESIGN.md DES-084: "Windows is not a shipped target;
// there Ensure compiles and does nothing"), so the no-op keeps the package
// building for cross-compilation checks and never reports busy.
func TryLock(_ string, fn func() error) (bool, error) {
	return false, fn()
}
