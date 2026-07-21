//go:build windows

package audit

// WithLock runs fn without file locking on Windows. Ethos targets Unix; the
// stub keeps the package building for cross-compilation checks.
func WithLock(_ string, fn func() error) error {
	return fn()
}
