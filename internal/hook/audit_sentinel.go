package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// auditSentinel is the minimal JSONL line emitted when the audit
// pipeline cannot persist a full entry. The shape is deliberately
// narrow — three fields only — so a permission, ENOSPC, or fsync
// failure that defeated the regular write has the best chance of
// admitting the sentinel write. Readers (`ethos audit show`, vox)
// see it through the same permissive decoder as a normal entry: the
// audit_error key falls into auditEntry's Unmarshal as an unknown
// field and is preserved at the JSONL line level.
//
// The struct exists separately from auditEntry because the sentinel
// is not a tool invocation. Carrying the auditEntry struct here would
// either inject empty tool/tool_input fields (noisy in audit show) or
// require omitempty on every field of auditEntry — a wider change
// than the sentinel warrants.
type auditSentinel struct {
	Ts         string `json:"ts"`
	Session    string `json:"session"`
	AuditError string `json:"audit_error"`
}

// emitAuditSentinel appends a sentinel JSONL line to the live audit file so a
// later `ethos audit show` reveals that an entry was lost. The caller holds the
// live-zone flock. Reason is the operator-facing description of what defeated
// the original entry write.
//
// The line is minted through AppendMonotonic (like the mission log), so it
// carries a strictly-monotonic ts above the seal watermark and the live file's
// own max. A raw wall-clock ts — as the original entry carried, second-
// precision — can land at or below an already-advanced monotonic floor, and
// then SelectLiveTail (seal) and LiveLinesPastWatermark (read) both filter it
// out: the loss marker itself would vanish. Routing through AppendMonotonic
// guarantees the marker sorts after every sealed and live line, so it is always
// sealed and always shown. AppendMonotonic appends in one write and fsyncs, the
// same atomicity the sentinel needs.
func emitAuditSentinel(livePath, sealedDir, legacyPath, sessionID string, now time.Time, reason string) error {
	// Watermark seeds the monotonic floor from sealed chunks + legacy; a
	// corrupt sealed dir makes it unavailable, in which case a zero floor still
	// lets AppendMonotonic mint above the live file's own max — a landed marker
	// beats no marker.
	watermark, wmErr := audit.Watermark(sealedDir, audit.SessionNS, "", legacyPath)
	if wmErr != nil {
		watermark = 0
	}
	_, err := audit.AppendMonotonic(livePath, watermark, now, func(ts int64) ([]byte, error) {
		data, mErr := json.Marshal(auditSentinel{
			Ts:         audit.FormatLineTS(ts),
			Session:    sessionID,
			AuditError: reason,
		})
		if mErr != nil {
			return nil, fmt.Errorf("marshaling sentinel: %w", mErr)
		}
		return data, nil
	})
	if err != nil {
		return fmt.Errorf("appending sentinel to %s: %w", livePath, err)
	}
	return nil
}

// emitLegacySentinel appends a sentinel line to a legacy single-tree audit file
// outside any repo, where there is no local zone to seal from and thus no
// watermark (docs/audit-seal.md §Migration). The entry's own ts is used as-is —
// with no seal there is nothing to sort below. Defensive shape: one canonical
// JSONL line, O_APPEND, single write, fsync.
func emitLegacySentinel(path, sessionID, ts, reason string) error {
	data, err := json.Marshal(auditSentinel{
		Ts:         ts,
		Session:    sessionID,
		AuditError: reason,
	})
	if err != nil {
		return fmt.Errorf("marshaling sentinel: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing sentinel to %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing sentinel %s: %w", path, err)
	}
	return nil
}
