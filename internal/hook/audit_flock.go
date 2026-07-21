package hook

import "github.com/punt-labs/ethos/internal/audit"

// WithLiveAuditLock executes fn while holding the per-session live-zone flock
// beside the live audit file (DES-058). Appends, monotonic-ts allocation, and
// seals in one checkout serialize on this one inode. Not re-entrant.
func WithLiveAuditLock(repoRoot, sessionID string, fn func() error) error {
	return audit.WithLock(liveAuditLockPath(repoRoot, sessionID), fn)
}
