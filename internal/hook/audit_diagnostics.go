package hook

import (
	"fmt"
	"io"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// AuditDiagnostics reports read-time conditions that are not audit entries but
// that a reader must see: quarantine gap markers (lines lost to corruption)
// and gitlink-deferred sessions (live lines past the watermark that no chunk
// yet records because the sealed tree is unreachable).
type AuditDiagnostics struct {
	Gaps     []audit.Gap
	Deferred []DeferredSession
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

		if !gitlink {
			continue
		}
		// A gitlink mount defers sealing, so the live tail past the watermark
		// is deliberately not yet in a chunk. Flag it with a count so the
		// reader knows the sealed record is temporarily incomplete.
		watermark, err := audit.Watermark(sealedDir, audit.SessionNS, "", sessionLegacyPath(sealedDir))
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

// WriteDiagnostics renders the diagnostics to w (stderr in the CLI). Gaps and
// gitlink-deferred sessions each get one line; an empty set writes nothing.
func (d AuditDiagnostics) WriteDiagnostics(w io.Writer) {
	for _, g := range d.Gaps {
		fmt.Fprintf(w, "gap: %s lost lines [%d,%d] to corruption (quarantined)\n", g.Chunk, g.First, g.Last)
	}
	for _, ds := range d.Deferred {
		fmt.Fprintf(w, "%s: %d unsealed lines, sealing deferred until vendored\n", ds.Session, ds.Unsealed)
	}
}
