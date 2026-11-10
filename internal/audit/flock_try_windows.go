//go:build windows

package audit

// TryLock runs fn without file locking on Windows, like WithLock.
func TryLock(_ string, fn func() error) (bool, error) {
	return false, fn()
}
