package hook

import (
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// LiveLineGuard returns the redaction guard for lines of a stream's live file:
// the mission-event guard for a mission log, the session-audit guard otherwise.
// The seal and the storage upgrade share it, so both rewrite a pre-redaction
// line the same way.
func LiveLineGuard(p layout.Policy, ns audit.Namespace) audit.LineGuard {
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	if ns == audit.MissionNS {
		return audit.LineGuard(missionLineGuard(checkout))
	}
	return audit.LineGuard(sessionLineGuard(checkout))
}
