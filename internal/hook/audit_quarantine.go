package hook

import (
	"fmt"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

// QuarantineChunk retires a corrupt sealed chunk named by its path and
// recovers what the live file still holds (DES-058 §Seal failure policy). It
// resolves the chunk's namespace, session, and live file from the path, then
// runs the quarantine under the matching live-zone flock so a concurrent seal
// or append cannot interleave.
//
// chunkPath is the path the seal/read error named — a session chunk under
// .punt-labs/ethos/sessions/<date>-<sid>/ or a mission chunk under
// .punt-labs/ethos/missions/<id>/. Returns the marker's summary for the CLI.
func QuarantineChunk(repoRoot, chunkPath, reason string) (audit.Marker, error) {
	// Quarantine is an operator command: with no usable redactor it
	// refuses rather than write a placeholder reason.
	pr, err := mission.NewPathRedactor(repoRoot)
	if err != nil {
		return audit.Marker{}, fmt.Errorf("quarantine: building path redactor: %w", err)
	}
	sealedDir := filepath.Dir(chunkPath)
	name := filepath.Base(chunkPath)

	if cn, kind := audit.Classify(name, audit.SessionNS); kind == audit.KindValid {
		sessionID := sessionIDFromDir(filepath.Base(sealedDir))
		if sessionID == "" {
			return audit.Marker{}, fmt.Errorf(
				"quarantine: cannot resolve session from directory %q", sealedDir)
		}
		livePath := liveAuditPath(repoRoot, sessionID)
		redactReason := pr.Content
		var marker audit.Marker
		err := WithLiveAuditLock(repoRoot, sessionID, func() error {
			var e error
			marker, e = audit.Quarantine(repoRoot, sealedDir, cn, livePath, reason, redactReason)
			return e
		})
		return marker, err
	}

	if cn, kind := audit.Classify(name, audit.MissionNS); kind == audit.KindValid {
		missionID := filepath.Base(sealedDir)
		livePath := liveMissionLogPath(repoRoot, missionID, cn.Session)
		redactReason := pr.Content
		var marker audit.Marker
		err := WithLiveMissionLock(repoRoot, missionID, cn.Session, func() error {
			var e error
			marker, e = audit.Quarantine(repoRoot, sealedDir, cn, livePath, reason, redactReason)
			return e
		})
		return marker, err
	}

	return audit.Marker{}, fmt.Errorf(
		"quarantine: %q is not a recognizable audit or mission chunk name", name)
}
