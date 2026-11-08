package audit

import (
	"fmt"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

var syncAuditDirectory = syncAuditDir

// Sync the name installation as well as newly created ancestors. Never sync
// above the operation's store/checkout roots. A failure after installation is
// visible, and leaves the installed bytes available for an idempotent retry.
func syncAuditParents(p layout.Policy, path string) error {
	store := filepath.Dir(filepath.Dir(filepath.Dir(p.LocalRoot())))
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if err := syncAuditDirectory(dir); err != nil {
			return fmt.Errorf("confirming audit directory durability at %s: %w", dir, err)
		}
		if dir == store || dir == checkout || filepath.Dir(dir) == dir {
			return nil
		}
	}
}

// ConfirmStreamDurability confirms the live name and newly created ancestors
// after a policy-aware appender has synced its payload, while holding the stream
// lock. Failure is an applied append with unconfirmed directory durability, not
// evidence that the line was rolled back.
func ConfirmStreamDurability(p layout.Policy, s Stream) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if _, err := p.LockPath("audit-stream", s.Key()); err != nil {
		return err
	}
	return syncAuditParents(p, s.LivePath(p))
}
