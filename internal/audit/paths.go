package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// timeFromUnixNanoDate renders a Unix-nanosecond timestamp as a UTC
// YYYY-MM-DD date.
func timeFromUnixNanoDate(ns int64) string {
	return time.Unix(0, ns).UTC().Format(SessionDateFormat)
}

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

// SessionDateFormat is the YYYY-MM-DD prefix on a dated per-session sealed
// directory. UTC by convention so two operators in different timezones see the
// same directory name for the same session.
const SessionDateFormat = "2006-01-02"

// LiveFirstLineDate returns the UTC date (YYYY-MM-DD) of a live audit file's
// first parseable line — the session's first-write day, the design's
// last-resort fallback for a sealed directory's date when the roster entry is
// gone (docs/audit-seal.md §Two zones). Empty when the file is absent or holds
// no parseable line.
func LiveFirstLineDate(livePath string) string {
	data, err := os.ReadFile(livePath)
	if err != nil {
		return ""
	}
	for _, raw := range SplitLines(data) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		if ns, perr := ParseLineTS(h.TS); perr == nil {
			return timeFromUnixNanoDate(ns)
		}
	}
	return ""
}

// MissionLive names one mission live-log file a session is expected to have
// written, plus whether it is present on disk.
type MissionLive struct {
	MissionID string
	LivePath  string
	Present   bool
}

// ExpectedMissionLiveFiles returns the per-(mission, session) live-log files a
// session is expected to have written, enumerated (not globbed) from the
// tracked mission chunks that carry the session's id: a sealed mission chunk
// proves the session wrote the live file, and live files are never deleted by
// design (docs/audit-seal.md §Seal failure policy). A file missing from disk
// is therefore evidence of loss, which a glob over extant files could never
// surface. Each entry records whether the expected file is present.
func ExpectedMissionLiveFiles(repoRoot, sessionID string) ([]MissionLive, error) {
	base := SealedMissionsBase(repoRoot)
	missions, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", base, err)
	}
	var out []MissionLive
	for _, d := range missions {
		if !d.IsDir() {
			continue
		}
		missionID := d.Name()
		if !missionChunkCarriesSession(filepath.Join(base, missionID), sessionID) {
			continue
		}
		livePath := LiveMissionLogPath(repoRoot, missionID, sessionID)
		_, statErr := os.Stat(livePath)
		out = append(out, MissionLive{MissionID: missionID, LivePath: livePath, Present: statErr == nil})
	}
	return out, nil
}

// missionChunkCarriesSession reports whether any valid mission chunk in dir
// carries the session's id.
func missionChunkCarriesSession(dir, sessionID string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if cn, kind := Classify(e.Name(), MissionNS); kind == KindValid && cn.Session == sessionID {
			return true
		}
	}
	return false
}

// MissionUnsealedCount returns how many lines a mission's per-(mission,
// session) live log holds past its sealed watermark. Zero when the live file
// is absent or fully sealed.
func MissionUnsealedCount(repoRoot, missionID, sessionID string) (int, error) {
	sealedDir := SealedMissionDir(repoRoot, missionID)
	legacy := filepath.Join(sealedDir, "log.jsonl")
	wm, err := Watermark(sealedDir, MissionNS, sessionID, legacy)
	if err != nil {
		return 0, err
	}
	tail, err := LiveLinesPastWatermark(LiveMissionLogPath(repoRoot, missionID, sessionID), sessionID, wm)
	if err != nil {
		return 0, err
	}
	return len(tail), nil
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
