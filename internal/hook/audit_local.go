package hook

import "github.com/punt-labs/ethos/internal/audit"

// The DES-058 live/sealed zone layout is canonical in internal/audit. These
// short package-local aliases keep the hook call sites terse.

func liveSessionsDir(repoRoot string) string { return audit.LiveSessionsDir(repoRoot) }

func liveAuditPath(repoRoot, sessionID string) string {
	return audit.LiveAuditPath(repoRoot, sessionID)
}

func liveAuditLockPath(repoRoot, sessionID string) string {
	return audit.LiveAuditLockPath(repoRoot, sessionID)
}

func liveMissionsDir(repoRoot string) string { return audit.LiveMissionsDir(repoRoot) }

func liveMissionLogPath(repoRoot, missionID, sessionID string) string {
	return audit.LiveMissionLogPath(repoRoot, missionID, sessionID)
}

func liveMissionLockPath(repoRoot, missionID, sessionID string) string {
	return audit.LiveMissionLockPath(repoRoot, missionID, sessionID)
}

func sealedSessionsBase(repoRoot string) string { return audit.SealedSessionsBase(repoRoot) }

func sealedMissionsBase(repoRoot string) string { return audit.SealedMissionsBase(repoRoot) }

func sealedMissionDir(repoRoot, missionID string) string {
	return audit.SealedMissionDir(repoRoot, missionID)
}
