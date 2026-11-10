package hook

import (
	"io"

	"github.com/punt-labs/ethos/v4/internal/auditimport"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// MigrateAuditWithPolicy consolidates associated legacy sessions in the shared
// local archive; see auditimport.MigrateWithPolicy.
func MigrateAuditWithPolicy(p layout.Policy, globalSessionsDir string, dryRun bool, out io.Writer) error {
	return auditimport.MigrateWithPolicy(p, globalSessionsDir, dryRun, out)
}
