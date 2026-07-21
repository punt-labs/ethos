package hook

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// WithLiveMissionLock executes fn while holding the per-(mission, session)
// live-zone flock beside the mission live log (DES-058 §Mission-tree churn).
// Each session that emits events for a mission serializes on its own inode.
func WithLiveMissionLock(repoRoot, missionID, sessionID string, fn func() error) error {
	return audit.WithLock(liveMissionLockPath(repoRoot, missionID, sessionID), fn)
}

// sealMissionsInRepo seals every (mission, session) live log tail in the repo
// and stages the chunks — the pre-commit backstop for the mission tree. Each
// session seals into the shared missions/<id>/ directory under its own
// per-(mission, session) flock and its own log-<session-id>-* chunk namespace.
func sealMissionsInRepo(repoRoot string, now time.Time, opts SealOptions, res *SealResult) error {
	missions, err := listRepoMissions(repoRoot)
	if err != nil {
		return err
	}
	for _, missionID := range missions {
		sessions, err := listMissionSessions(repoRoot, missionID)
		if err != nil {
			return err
		}
		for _, sessionID := range sessions {
			if err := sealMissionSession(repoRoot, missionID, sessionID, now, opts, res); err != nil {
				return fmt.Errorf("sealing mission %s session %s: %w", missionID, sessionID, err)
			}
		}
	}
	return nil
}

// sealMissionSession seals one (mission, session) live log tail under its
// flock and stages the mission dir's chunks for that session.
func sealMissionSession(repoRoot, missionID, sessionID string, now time.Time, opts SealOptions, res *SealResult) error {
	sealedDir := sealedMissionDir(repoRoot, missionID)
	var out sealOutcome
	lockErr := WithLiveMissionLock(repoRoot, missionID, sessionID, func() error {
		var e error
		out, e = sealDirLocked(sealDirParams{
			repoRoot:  repoRoot,
			ns:        audit.MissionNS,
			session:   sessionID,
			sealedDir: sealedDir,
			livePath:  liveMissionLogPath(repoRoot, missionID, sessionID),
			legacy:    []string{missionLegacyLogPath(sealedDir)},
			chunkName: func(first, last int64) string { return audit.MissionChunkFile(sessionID, first, last) },
			tempName:  func(first, last int64) string { return audit.MissionTempFile(sessionID, first, last) },
			label:     "mission " + missionID + " session " + sessionID,
		}, now, opts)
		return e
	})
	if lockErr != nil {
		return lockErr
	}
	return tallyAndStage(repoRoot, sealedDir, audit.MissionNS, sessionID, out, opts, res)
}

// missionLegacyLogPath returns the frozen legacy log.jsonl in a mission's
// tracked directory — the pre-DES-058 committed mission log, read as the
// mission's oldest chunk and never rewritten.
func missionLegacyLogPath(sealedMissionDir string) string {
	return filepath.Join(sealedMissionDir, "log.jsonl")
}
