//go:build windows

package hook

// WithLiveAuditLock runs fn without file locking on Windows, matching the
// identity package's flock stub. Ethos targets Unix; the stub keeps the
// package building for cross-compilation checks.
func WithLiveAuditLock(_, _ string, fn func() error) error {
	return fn()
}
