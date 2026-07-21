package hook

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/punt-labs/ethos/internal/audit"
	"github.com/punt-labs/ethos/internal/mission"
)

// VacuumCrossCheck guards the seal's silent-vacuum case (docs/audit-seal.md
// §Seal failure policy): a seal that touches nothing must still notice a
// session whose unsealed audit lines were lost. It is per session and
// iterates two sources — each purge tombstone whose recorded repo is this
// repo and that carries an unsealed-lines flag, and each roster-active session
// bound to this repo — warning on stderr for any whose recorded live file is
// absent (a checkout deleted with unsealed lines) or still holds unsealed
// lines. It never blocks (the caller stays exit 0); it only refuses to let a
// lost live file pass unremarked.
//
// The tombstone branch is what keeps the crash -> purge -> checkout-deleted ->
// commit sequence from going silent: purge removed the roster entry the
// per-session check would otherwise have visited.
//
// globalRoot is ~/.punt-labs/ethos; the per-session tombstones, rosters, and
// mission-claim sidecars all live under <globalRoot>/sessions.
func VacuumCrossCheck(repoRoot, globalRoot string, activeSessions []string, w io.Writer) error {
	globalSessionsDir := filepath.Join(globalRoot, "sessions")
	tombstones, err := audit.ListTombstones(globalSessionsDir)
	if err != nil {
		return err
	}
	for _, t := range tombstones {
		if t.Repo != repoRoot || !t.Flagged() {
			continue
		}
		if !audit.SessionLiveFileExists(repoRoot, t.Session) {
			fmt.Fprintf(w,
				"warning: session %s was purged with unsealed audit lines and its live file is gone; "+
					"those lines are lost. Acknowledge with `ethos session purge --ack %s`\n",
				t.Session, t.Session)
		} else {
			n, cErr := audit.SessionUnsealedCount(repoRoot, t.Session)
			if cErr != nil {
				return cErr
			}
			if n > 0 {
				fmt.Fprintf(w,
					"warning: session %s was purged with %d unsealed audit line(s) still on disk; "+
						"commit to seal them. Acknowledge with `ethos session purge --ack %s`\n",
					t.Session, n, t.Session)
			}
		}
		// A purged session's mission-log lines are guarded the same way: an
		// expected mission live file gone is a lost mission-log record.
		if mErr := warnMissingMissionLives(globalRoot, repoRoot, t.Session, w); mErr != nil {
			return mErr
		}
	}

	// Roster-active sessions bound to this repo whose recorded live file has
	// vanished — a single deleted live file with no purge to leave a tombstone,
	// in either the audit or the mission namespace (REQ-1: the guard is per
	// session ACROSS BOTH namespaces, not audit-only).
	for _, sessionID := range activeSessions {
		if !audit.SessionLiveFileExists(repoRoot, sessionID) {
			fmt.Fprintf(w,
				"warning: active session %s has no live audit file in this repo; "+
					"if it was deleted, unsealed lines were lost\n",
				sessionID)
		}
		if mErr := warnMissingMissionLives(globalRoot, repoRoot, sessionID, w); mErr != nil {
			return mErr
		}
	}
	return nil
}

// warnMissingMissionLives warns for each of a session's expected mission live
// files that is absent from disk — a lost mission-log record the audit-only
// check would miss (REQ-1). The expected set unions the tracked mission chunks
// carrying the session id with the missions the session is bound to in mission
// records (claim sidecar + delegation records), so a Tier B session that
// claimed a mission but sealed no chunk is still enumerated.
func warnMissingMissionLives(globalRoot, repoRoot, sessionID string, w io.Writer) error {
	bound, err := mission.SessionBoundMissions(globalRoot, repoRoot, sessionID)
	if err != nil {
		return err
	}
	expected, err := audit.ExpectedMissionLiveFiles(repoRoot, sessionID, bound)
	if err != nil {
		return err
	}
	for _, ml := range expected {
		if !ml.Present {
			fmt.Fprintf(w,
				"warning: session %s wrote mission-log lines for mission %s but its mission live log is gone; "+
					"unsealed mission-log lines were lost\n",
				sessionID, ml.MissionID)
		}
	}
	return nil
}
