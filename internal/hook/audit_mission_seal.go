package hook

import (
	"fmt"
	"os"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// WithLiveMissionLock executes fn while holding the per-(mission, session)
// live-zone flock beside the mission live log (DES-058 §Mission-tree churn).
// Each session that emits events for a mission serializes on its own inode.
func WithLiveMissionLock(repoRoot, missionID, sessionID string, fn func() error) error {
	return audit.WithLock(liveMissionLockPath(repoRoot, missionID, sessionID), fn)
}

// SealMission seals every session's not-yet-sealed log tail under one
// mission's directory and stages the chunks (DES-058 §Seal triggers, the
// mission-close secondary). It is the authoritative seal for the closing
// checkout: cross-checkout worker tails ride their own checkouts' triggers.
// A gitlink-mounted repo defers with a one-line stderr notice.
func SealMission(repoRoot, missionID string, now time.Time, opts SealOptions) (SealResult, error) {
	gitlink, err := audit.IsGitlinkMount(repoRoot)
	if err != nil {
		return SealResult{}, SealGitError(repoRoot, "checking for a gitlink mount (git ls-files)", err, true)
	}
	if gitlink {
		fmt.Fprintf(os.Stderr,
			"ethos: mission close: sealing deferred: .punt-labs/ethos is a gitlink mount, pending e29s (%s)\n",
			repoRoot)
		return SealResult{Deferred: true}, nil
	}
	sessions, err := listMissionSessions(repoRoot, missionID)
	if err != nil {
		return SealResult{}, err
	}
	units := make([]sealUnit, 0, len(sessions))
	for _, sessionID := range sessions {
		units = append(units, missionSealUnit(repoRoot, missionID, sessionID, now, opts))
	}
	var res SealResult
	if err := runSeal(repoRoot, units, opts, &res); err != nil {
		return res, err
	}
	return res, nil
}

// missionSealUnit is one (mission, session) live log tail: sealed under its
// flock into the mission dir's chunks for that session.
func missionSealUnit(repoRoot, missionID, sessionID string, now time.Time, opts SealOptions) sealUnit {
	sealedDir := sealedMissionDir(repoRoot, missionID)
	return sealUnit{
		label: "mission " + missionID + " session " + sessionID, ns: audit.MissionNS,
		session: sessionID, sealedDir: sealedDir,
		write: func() (sealOutcome, error) {
			var out sealOutcome
			err := WithLiveMissionLock(repoRoot, missionID, sessionID, func() error {
				var e error
				out, e = sealDirLocked(sealDirParams{
					repoRoot:  repoRoot,
					ns:        audit.MissionNS,
					session:   sessionID,
					sealedDir: sealedDir,
					livePath:  liveMissionLogPath(repoRoot, missionID, sessionID),
					chunkName: func(first, last int64) string { return audit.MissionChunkFile(sessionID, first, last) },
					tempName:  func(first, last int64) string { return audit.MissionTempFile(sessionID, first, last) },
					label:     "mission " + missionID + " session " + sessionID,
				}, now, opts)
				return e
			})
			return out, err
		},
	}
}
