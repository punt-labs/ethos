package hook

import "time"

// SealMission archives one mission's streams under the canonical store root.
// Linked-checkout callers use SealMissionWithPolicy with their roots snapshot.
func SealMission(repoRoot, missionID string, now time.Time, opts SealOptions) (SealResult, error) {
	p, err := auditPolicy(repoRoot)
	if err != nil {
		return SealResult{}, err
	}
	return SealMissionWithPolicy(p, missionID, now, opts)
}
