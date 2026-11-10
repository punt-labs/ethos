package hook

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// appendLiveAuditReport allocates a strictly-monotonic per-session timestamp and
// appends one redacted line to the live audit file. The caller must hold the
// per-session live-zone flock (WithLiveAuditLock); allocation and append
// happen under one lock so the timestamp is strictly increasing and never
// repeats within a session (docs/audit-seal.md §timestamp).
//
// The floor is seeded from the seal watermark (sealed chunks + covering
// markers + the frozen legacy file's max ts) so no minted ts sinks below an
// already-sealed line. Returns the entry with its allocated Ts set.
// It returns what the sink guard did to the line, for the writer's one
// notice; the guard does not print here.
func appendLiveAuditReport(livePath, sealedDir, legacyPath string, entry auditEntry, now time.Time) (auditEntry, guardReport, error) {
	var report guardReport
	floor, err := audit.MonotonicFloor(sealedDir, audit.SessionNS, "", legacyPath)
	if err != nil {
		return entry, report, fmt.Errorf("computing monotonic floor for %s: %w", livePath, err)
	}
	ts, err := audit.AppendMonotonic(livePath, floor, now, func(ts int64) ([]byte, error) {
		entry.Ts = audit.FormatLineTS(ts)
		data, mErr := json.Marshal(entry)
		if mErr != nil {
			return nil, fmt.Errorf("marshaling entry: %w", mErr)
		}
		out, rule, marker := guardAuditLineFull(data)
		report = guardReport{Rule: rule, Marker: marker}
		return out, nil
	})
	if err != nil {
		return entry, report, err
	}
	entry.Ts = audit.FormatLineTS(ts)
	return entry, report, nil
}
