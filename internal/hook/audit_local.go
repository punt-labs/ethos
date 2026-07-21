package hook

import "path/filepath"

// The live write path and the sealed record live in two zones of the
// same checkout (DES-058). The live zone under .punt-labs/local/ is
// gitignored and machine-local; the sealed zone under .punt-labs/ethos/
// is git-tracked. These helpers name the paths in both zones.

// localZoneBase is the machine-local, gitignored root inside a checkout.
func localZoneBase(repoRoot string) string {
	return filepath.Join(repoRoot, ".punt-labs", "local", "ethos")
}

// liveSessionsDir is the live zone for session audit files.
func liveSessionsDir(repoRoot string) string {
	return filepath.Join(localZoneBase(repoRoot), "sessions")
}

// liveAuditPath returns the live session audit file: the per-checkout
// gitignored file the writer appends to. Flat name (no date prefix) —
// the dated directory belongs to the sealed record, not the live file.
func liveAuditPath(repoRoot, sessionID string) string {
	return filepath.Join(liveSessionsDir(repoRoot), filepath.Base(sessionID)+".audit.jsonl")
}

// liveAuditLockPath returns the per-session flock beside the live audit
// file. DES-058 moves the append/seal lock out of the global tree to sit
// next to the file it serializes, so appends and seals in one checkout
// contend on one inode.
func liveAuditLockPath(repoRoot, sessionID string) string {
	return filepath.Join(liveSessionsDir(repoRoot), filepath.Base(sessionID)+".lock")
}

// liveMissionsDir is the live zone for mission logs.
func liveMissionsDir(repoRoot string) string {
	return filepath.Join(localZoneBase(repoRoot), "missions")
}

// liveMissionLogPath returns a per-(mission, session) live log file. Each
// session appending events for a mission writes its own file, so two
// sessions never contend and their sealed chunks never collide.
func liveMissionLogPath(repoRoot, missionID, sessionID string) string {
	return filepath.Join(liveMissionsDir(repoRoot), filepath.Base(missionID),
		filepath.Base(sessionID)+".log.jsonl")
}

// liveMissionLockPath returns the per-(mission, session) flock beside the
// mission live log.
func liveMissionLockPath(repoRoot, missionID, sessionID string) string {
	return filepath.Join(liveMissionsDir(repoRoot), filepath.Base(missionID),
		filepath.Base(sessionID)+".lock")
}

// sealedSessionsBase is the tracked zone holding dated per-session
// directories of sealed audit chunks.
func sealedSessionsBase(repoRoot string) string {
	return filepath.Join(repoRoot, ".punt-labs", "ethos", "sessions")
}

// sealedMissionsBase is the tracked zone holding per-mission directories
// of sealed log chunks.
func sealedMissionsBase(repoRoot string) string {
	return filepath.Join(repoRoot, ".punt-labs", "ethos", "missions")
}

// sealedMissionDir returns a mission's tracked sealed directory.
func sealedMissionDir(repoRoot, missionID string) string {
	return filepath.Join(sealedMissionsBase(repoRoot), filepath.Base(missionID))
}
