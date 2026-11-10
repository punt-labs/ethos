package hook

import "github.com/punt-labs/ethos/v4/internal/audit"

// The DES-058 live/sealed zone layout is canonical in internal/audit. These
// short package-local aliases keep the hook call sites terse.

func liveSessionsDir(repoRoot string) string { return audit.LiveSessionsDir(repoRoot) }

func liveAuditPath(repoRoot, sessionID string) string {
	return audit.LiveAuditPath(repoRoot, sessionID)
}

func liveMissionLogPath(repoRoot, missionID, sessionID string) string {
	return audit.LiveMissionLogPath(repoRoot, missionID, sessionID)
}

func sealedSessionsBase(repoRoot string) string { return audit.SealedSessionsBase(repoRoot) }

func sealedMissionDir(repoRoot, missionID string) string {
	return audit.SealedMissionDir(repoRoot, missionID)
}
