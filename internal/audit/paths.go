package audit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The live write path and the sealed record live in two zones of the same
// checkout (DES-058). The live zone under .punt-labs/local/ is gitignored and
// machine-local; the sealed zone under .punt-labs/ethos/ is git-tracked.
// These helpers are the canonical layout, shared by the session audit log and
// the mission event log.

// LocalZoneBase is the machine-local, gitignored root inside a checkout.
func LocalZoneBase(repoRoot string) string {
	return filepath.Join(repoRoot, ".punt-labs", "local", "ethos")
}

// LiveSessionsDir is the live zone for session audit files.
func LiveSessionsDir(repoRoot string) string {
	return filepath.Join(LocalZoneBase(repoRoot), "sessions")
}

// LiveAuditPath returns the live session audit file the writer appends to.
func LiveAuditPath(repoRoot, sessionID string) string {
	return filepath.Join(LiveSessionsDir(repoRoot), filepath.Base(sessionID)+".audit.jsonl")
}

// LiveAuditLockPath returns the per-session flock beside the live audit file.
func LiveAuditLockPath(repoRoot, sessionID string) string {
	return filepath.Join(LiveSessionsDir(repoRoot), filepath.Base(sessionID)+".lock")
}

// LiveMissionsDir is the live zone for mission logs.
func LiveMissionsDir(repoRoot string) string {
	return filepath.Join(LocalZoneBase(repoRoot), "missions")
}

// LiveMissionLogPath returns a per-(mission, session) live log file. Each
// session appending events for a mission writes its own file, so two sessions
// never contend and their sealed chunks never collide.
func LiveMissionLogPath(repoRoot, missionID, sessionID string) string {
	return filepath.Join(LiveMissionsDir(repoRoot), filepath.Base(missionID),
		filepath.Base(sessionID)+".log.jsonl")
}

// LiveMissionLockPath returns the per-(mission, session) flock beside the
// mission live log.
func LiveMissionLockPath(repoRoot, missionID, sessionID string) string {
	return filepath.Join(LiveMissionsDir(repoRoot), filepath.Base(missionID),
		filepath.Base(sessionID)+".lock")
}

// SealedSessionsBase is the tracked zone holding dated per-session
// directories of sealed audit chunks.
func SealedSessionsBase(repoRoot string) string {
	return filepath.Join(repoRoot, ".punt-labs", "ethos", "sessions")
}

// SealedMissionsBase is the tracked zone holding per-mission directories of
// sealed log chunks.
func SealedMissionsBase(repoRoot string) string {
	return filepath.Join(repoRoot, ".punt-labs", "ethos", "missions")
}

// SealedMissionDir returns a mission's tracked sealed directory.
func SealedMissionDir(repoRoot, missionID string) string {
	return filepath.Join(SealedMissionsBase(repoRoot), filepath.Base(missionID))
}

// FindSealedSessionDir returns the existing dated sealed directory for a
// session (any date prefix), or "" when none exists yet. Both the seal and
// the purge check resolve a session's sealed directory through this so a
// session whose start date differs from today still resolves to one place.
func FindSealedSessionDir(repoRoot, sessionID string) (string, error) {
	base := SealedSessionsBase(repoRoot)
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", base, err)
	}
	suffix := "-" + filepath.Base(sessionID)
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), suffix) {
			return filepath.Join(base, e.Name()), nil
		}
	}
	return "", nil
}

// SessionUnsealedCount returns how many live audit lines a session holds past
// its sealed watermark — the lines a purge would strand. Zero when the live
// file is absent or fully sealed.
func SessionUnsealedCount(repoRoot, sessionID string) (int, error) {
	dir, err := FindSealedSessionDir(repoRoot, sessionID)
	if err != nil {
		return 0, err
	}
	var legacy string
	if dir != "" {
		legacy = filepath.Join(dir, "audit.jsonl")
	}
	wm, err := Watermark(dir, SessionNS, "", legacy)
	if err != nil {
		return 0, err
	}
	tail, err := LiveLinesPastWatermark(LiveAuditPath(repoRoot, sessionID), "", wm)
	if err != nil {
		return 0, err
	}
	return len(tail), nil
}

// SessionLiveFileExists reports whether a session's recorded live audit file
// is present. An absent recorded live file at purge time is itself evidence —
// a checkout deleted before its lines sealed.
func SessionLiveFileExists(repoRoot, sessionID string) bool {
	_, err := os.Stat(LiveAuditPath(repoRoot, sessionID))
	return err == nil
}

// ListLiveLogSessions returns the session ids whose live mission log files
// (<session-id>.log.jsonl) exist in dir, sorted. A missing directory yields
// nil.
func ListLiveLogSessions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		const suffix = ".log.jsonl"
		if len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
			ids = append(ids, name[:len(name)-len(suffix)])
		}
	}
	sort.Strings(ids)
	return ids, nil
}
