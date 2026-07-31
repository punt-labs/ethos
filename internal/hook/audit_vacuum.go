package hook

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/punt-labs/ethos/internal/audit"
	"github.com/punt-labs/ethos/internal/mission"
)

// ActiveSession names a roster-active session and the checkout its roster
// recorded as the writer of its live zone. An empty Checkout is a roster
// written before the field existed: the writer is unknown, so no checkout has
// standing to call that session's live files lost.
type ActiveSession struct {
	Session  string
	Checkout string
}

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
//
// A tombstone's Repo is a git-remote identity (org/name), not a checkout path,
// so it is matched against this checkout's identity — derived by the same parser
// session-start used. A checkout with no parseable origin has identity "",
// which matches the "" its own sessions recorded. Two checkouts of one repo
// share an identity, which is why the identity alone cannot say where a
// session's live files are: each source carries its own recorded checkout path
// and the live probes follow it, while the tracked chunks are read from
// repoRoot, the committing checkout.
func VacuumCrossCheck(repoRoot, globalRoot string, activeSessions []ActiveSession, w io.Writer) error {
	repoID := audit.RepoIdentity(repoRoot)
	globalSessionsDir := filepath.Join(globalRoot, "sessions")
	tombstones, err := audit.ListTombstones(globalSessionsDir, w)
	if err != nil {
		return err
	}
	for _, t := range tombstones {
		if t.Repo != repoID || !t.Flagged() {
			continue
		}
		// The tombstone records the checkout it was purged from, and the live
		// zone lives under that checkout — so that is where the live files are
		// stat'd (DESIGN.md §Seal failure). A tombstone written before the
		// field existed names no checkout; fall back to this one, which is what
		// the pre-field code always did.
		writer := audit.RecordedWriter(t.Checkout)
		if t.Checkout == "" {
			writer = audit.AssumedWriter(repoRoot)
		}
		liveRoot := writer.Root
		if !audit.SessionLiveFileExists(liveRoot, t.Session) {
			fmt.Fprintf(w,
				"warning: session %s was purged with unsealed audit lines and its live file is gone; "+
					"those lines are lost. Acknowledge with `ethos session purge --ack %s`\n",
				t.Session, t.Session)
		} else {
			n, cErr := audit.SessionUnsealedCountAcross(repoRoot, liveRoot, t.Session)
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
		// A purged session's mission-log lines are guarded the same way.
		if mErr := warnMissingMissionLives(globalRoot, repoRoot, writer, t.Session, w); mErr != nil {
			return mErr
		}
	}

	// Roster-active sessions bound to this repo whose recorded live file has
	// vanished — a single deleted live file with no purge to leave a tombstone,
	// in either the audit or the mission namespace (REQ-1: the guard is per
	// session ACROSS BOTH namespaces, not audit-only). Each is probed in the
	// checkout its roster recorded, never in whichever checkout happens to be
	// committing.
	unrecorded := 0
	for _, as := range activeSessions {
		if as.Checkout == "" {
			unrecorded++
			continue
		}
		if !audit.SessionLiveFileExists(as.Checkout, as.Session) {
			fmt.Fprintf(w,
				"warning: active session %s has no live audit file in %s; "+
					"if it was deleted, unsealed lines were lost\n",
				as.Session, as.Checkout)
		}
		if mErr := warnMissingMissionLives(globalRoot, repoRoot, audit.RecordedWriter(as.Checkout), as.Session, w); mErr != nil {
			return mErr
		}
	}
	// Skipping those sessions is correct — nothing can be concluded about a
	// live zone whose location is unrecorded — but a guard that quietly covers
	// fewer sessions than it appears to is its own hazard. One aggregate line,
	// not one per session: the condition clears for a roster the next time its
	// session starts, and for a dead one when it is purged.
	if unrecorded > 0 {
		fmt.Fprintf(w,
			"note: %d active session(s) predate the roster checkout field, so their live files "+
				"cannot be located and are not checked; `ethos session purge` clears dead ones\n",
			unrecorded)
	}
	return nil
}

// warnMissingMissionLives warns for each of a session's expected mission live
// files whose lines are unaccounted for — a lost mission-log record the
// audit-only check would miss (REQ-1). The expected set unions the tracked
// mission chunks carrying the session id with the missions the session is bound
// to in mission records (claim sidecar + delegation records), so a Tier B
// session that claimed a mission but sealed no chunk is still enumerated.
//
// The warning is MissionLive.Lost, not mere absence. An absent live file beside
// a sealed chunk is the steady state of every checkout that did not write it —
// chunks are tracked and travel, live files are per-checkout and never do — so
// warning on absence alone reported loss for every mission a long-lived session
// had ever touched, in every other checkout (ethos-q6e2).
func warnMissingMissionLives(globalRoot, repoRoot string, writer audit.Writer, sessionID string, w io.Writer) error {
	bound, err := mission.SessionBoundMissions(globalRoot, repoRoot, sessionID)
	if err != nil {
		return err
	}
	expected, err := audit.ExpectedMissionLiveFiles(repoRoot, writer, sessionID, bound)
	if err != nil {
		return err
	}
	for _, ml := range expected {
		if ml.Lost() {
			fmt.Fprintf(w,
				"warning: session %s wrote mission-log lines for mission %s but its mission live log is gone; "+
					"unsealed mission-log lines were lost\n",
				sessionID, ml.MissionID)
		}
	}
	return nil
}
