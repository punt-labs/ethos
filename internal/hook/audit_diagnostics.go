package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// AuditDiagnostics reports read-time conditions that are not audit entries but
// that a reader must see: quarantine gap markers (lines lost to corruption),
// gitlink-deferred sessions (live lines past the watermark that no chunk yet
// records because the sealed tree is unreachable), and loss markers (sentinel
// lines an audit write left behind when it could not persist a full entry).
type AuditDiagnostics struct {
	Gaps        []audit.Gap
	Deferred    []DeferredSession
	LossMarkers []LossMarker
}

// LossMarker names an audit_error sentinel in a session's stream — a point
// where the audit pipeline could not persist a full entry (docs/audit-seal.md
// §Seal failure policy). `ethos audit show`'s entry rendering drops the
// audit_error field (auditEntry has no such field), so this diagnostic is the
// operator-facing surface for the loss.
type LossMarker struct {
	Session string
	Ts      string
	Error   string
}

// DeferredSession names a session whose live tail sits past the sealed
// watermark in a gitlink-mounted repo, with the count of unsealed lines.
type DeferredSession struct {
	Session  string
	Unsealed int
}

// CollectAuditDiagnostics walks every repo session and gathers the read-time
// diagnostics for `ethos audit show`: quarantine gaps in each session's sealed
// directory, and — in a gitlink-mounted repo — the count of live lines past
// the watermark that the deferred seal has not recorded.
func CollectAuditDiagnostics(repoRoot string, now time.Time) (AuditDiagnostics, error) {
	var diag AuditDiagnostics
	sessions, err := listRepoSessions(repoRoot)
	if err != nil {
		return diag, err
	}
	gitlink := audit.IsGitlinkMount(repoRoot)
	for _, sessionID := range sessions {
		sealedDir, err := resolveRepoSessionDir(repoRoot, sessionID, now)
		if err != nil {
			return diag, err
		}
		gaps, err := audit.GapMarkers(sealedDir, audit.SessionNS, "")
		if err != nil {
			return diag, err
		}
		diag.Gaps = append(diag.Gaps, gaps...)

		losses, err := sessionLossMarkers(repoRoot, sessionID, now)
		if err != nil {
			return diag, err
		}
		diag.LossMarkers = append(diag.LossMarkers, losses...)

		if !gitlink {
			continue
		}
		// A gitlink mount defers sealing, so the live tail past the watermark
		// is deliberately not yet in a chunk. Flag it with a count so the
		// reader knows the sealed record is temporarily incomplete.
		watermark, err := audit.Watermark(sealedDir, audit.SessionNS, "")
		if err != nil {
			return diag, err
		}
		tail, err := audit.LiveLinesPastWatermark(liveAuditPath(repoRoot, sessionID), "", watermark)
		if err != nil {
			return diag, err
		}
		if len(tail) > 0 {
			diag.Deferred = append(diag.Deferred, DeferredSession{Session: sessionID, Unsealed: len(tail)})
		}
	}
	return diag, nil
}

// sessionLossMarkers scans a session's full union stream (sealed chunks + live
// tail + legacy, kept raw) for audit_error sentinel lines and returns one
// marker per sentinel found. Reusing sessionUnionLines keeps the scan on the
// same partition the entry read walks, so a sentinel is surfaced exactly once
// whether it is still in the live tail or already sealed into a chunk.
func sessionLossMarkers(repoRoot, sessionID string, now time.Time) ([]LossMarker, error) {
	lines, err := sessionUnionLines(repoRoot, sessionID, now)
	if err != nil {
		return nil, err
	}
	var out []LossMarker
	for _, l := range lines {
		var s auditSentinel
		if json.Unmarshal(l.Raw, &s) != nil || s.AuditError == "" {
			continue
		}
		out = append(out, LossMarker{Session: sessionID, Ts: s.Ts, Error: s.AuditError})
	}
	return out, nil
}

// WriteDiagnostics renders the diagnostics to w (stderr in the CLI). Gaps,
// gitlink-deferred sessions, and loss markers each get one line; an empty set
// writes nothing.
func (d AuditDiagnostics) WriteDiagnostics(w io.Writer) {
	for _, g := range d.Gaps {
		fmt.Fprintf(w, "gap: %s lost lines [%d,%d] to corruption (quarantined)\n", g.Chunk, g.First, g.Last)
	}
	for _, ds := range d.Deferred {
		fmt.Fprintf(w, "%s: %d unsealed lines, sealing deferred until vendored\n", ds.Session, ds.Unsealed)
	}
	for _, lm := range d.LossMarkers {
		fmt.Fprintf(w, "loss: session %s ts %s: an audit entry was not persisted (%s)\n", lm.Session, lm.Ts, lm.Error)
	}
}
