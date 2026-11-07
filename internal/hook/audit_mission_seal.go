package hook

import (
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// WithLiveMissionLock executes fn while holding the per-(mission, session)
// live-zone flock beside the mission live log (DES-058 §Mission-tree churn).
// Each session that emits events for a mission serializes on its own inode.
func WithLiveMissionLock(repoRoot, missionID, sessionID string, fn func() error) error {
	return audit.WithLock(liveMissionLockPath(repoRoot, missionID, sessionID), fn)
}

// SealMission archives one mission's streams under the canonical store root.
// Linked-checkout callers use SealMissionWithPolicy with their roots snapshot.
func SealMission(repoRoot, missionID string, now time.Time, opts SealOptions) (SealResult, error) {
	p, err := auditPolicy(repoRoot)
	if err != nil {
		return SealResult{}, err
	}
	return SealMissionWithPolicy(p, missionID, now, opts)
}
