package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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

// MissionResiduePath returns the superseded shared-live design's per-checkout
// missions/<id>.jsonl residue in the local zone. That design's seal copied
// its lines into chunks, so it is NOT the frozen legacy log.jsonl: it is
// drained once as a pre-discipline legacy source, ordered after the tracked
// log.jsonl (docs/audit-seal.md §Migration).
func MissionResiduePath(repoRoot, missionID string) string {
	return filepath.Join(LiveMissionsDir(repoRoot), filepath.Base(missionID)+".jsonl")
}

// MissionLegacyLogPath returns a mission's frozen pre-DES-058 tracked event
// log — the record a mission closed before the live/sealed split carries
// instead of chunks. It is git-tracked, so it reaches every checkout.
func MissionLegacyLogPath(repoRoot, missionID string) string {
	return filepath.Join(SealedMissionDir(repoRoot, missionID), "log.jsonl")
}

// MissionLegacySources returns a mission's frozen pre-discipline sources in
// read order: the tracked log.jsonl first, then the drained missions/<id>.jsonl
// residue. Both contribute their max ts to the mission watermark and pass
// through the read undeduped as the mission's oldest lines.
func MissionLegacySources(repoRoot, missionID string) []string {
	return []string{
		MissionLegacyLogPath(repoRoot, missionID),
		MissionResiduePath(repoRoot, missionID),
	}
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
	want := filepath.Base(sessionID)
	for _, e := range entries {
		if e.IsDir() && SessionDirMatches(e.Name(), want) {
			return filepath.Join(base, e.Name()), nil
		}
	}
	return "", nil
}

// SessionDirMatches reports whether name is exactly <YYYY-MM-DD>-<sessionID>: a
// valid 10-char date, the hyphen separator, then the session id verbatim. This
// is the single matcher every session-directory resolver shares — the sealed
// tree's seal and read (FindSealedSessionDir) and the live tree's writer,
// reader, and migrate (hook.findSessionDir) — so seal and read never resolve a
// colliding id to different directories. A bare "-"+sessionID suffix match
// would let id "abc" match "2026-07-21-x-abc", landing one session's chunks in
// another's tree with a wrong watermark.
func SessionDirMatches(name, sessionID string) bool {
	const dateLen = len(SessionDateFormat)
	if len(name) != dateLen+1+len(sessionID) {
		return false
	}
	if name[dateLen] != '-' {
		return false
	}
	if _, err := time.Parse(SessionDateFormat, name[:dateLen]); err != nil {
		return false
	}
	return name[dateLen+1:] == sessionID
}

// The two zones of DES-058 sit in two different roots whenever one checkout
// probes a session that another one wrote. Sealed chunks are git-tracked and
// reach every checkout, so they are read from trackedRoot — the checkout doing
// the probing. The live file is machine-local and reaches none of them, so it
// is read from liveRoot — the checkout that recorded itself as the writer. The
// two roots are the same directory in the ordinary case, and a probe that
// conflates them is the ethos-q6e2 defect.

// SessionUnsealedCountAcross returns how many live audit lines a session holds
// past its sealed watermark when the sealed record and the live file live in
// different checkouts. Zero when the live file is absent or fully sealed.
func SessionUnsealedCountAcross(trackedRoot, liveRoot, sessionID string) (int, error) {
	dir, err := FindSealedSessionDir(trackedRoot, sessionID)
	if err != nil {
		return 0, err
	}
	wm, err := Watermark(dir, SessionNS, "")
	if err != nil {
		return 0, err
	}
	tail, err := LiveLinesPastWatermark(LiveAuditPath(liveRoot, sessionID), "", wm)
	if err != nil {
		return 0, err
	}
	return len(tail), nil
}

// SessionUnsealedCount is SessionUnsealedCountAcross for a session probed in
// the checkout that wrote it — the lines a purge there would strand.
func SessionUnsealedCount(repoRoot, sessionID string) (int, error) {
	return SessionUnsealedCountAcross(repoRoot, repoRoot, sessionID)
}

// SessionLiveFileExists reports whether a session's recorded live audit file is
// present in liveRoot, the checkout that recorded itself as its writer. An
// absent live file THERE is evidence — a checkout deleted before its lines
// sealed. Its absence anywhere else says nothing.
func SessionLiveFileExists(liveRoot, sessionID string) bool {
	_, err := os.Stat(LiveAuditPath(liveRoot, sessionID))
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
// written, plus the evidence bearing on whether its lines survived.
type MissionLive struct {
	MissionID string
	LivePath  string
	// Present reports whether the live file is on disk in the probed checkout.
	Present bool
	// Sealed reports whether the mission's tracked chunks (or a covering
	// quarantine marker) already carry this session's lines.
	Sealed bool
	// Legacy reports whether the mission is wholly pre-DES-058: a frozen
	// record — the tracked missions/<id>/log.jsonl or the drained
	// per-checkout residue — holds its lines and no session ever sealed a
	// chunk for it.
	//
	// A frozen record alone is not enough. Legacy lines carry no session
	// attribution, so unlike Sealed this cannot be filtered to one session;
	// a mission worked both before and after the split would let its old
	// log.jsonl vouch for a later session whose live log really was lost.
	// Requiring the mission to hold no chunk from ANY session keeps the
	// proof to missions that closed before the split existed.
	Legacy bool
	// WriterZone reports whether the probed checkout's live-missions zone
	// exists at all. It separates "this checkout never had these files" from
	// "this checkout had them and this one is missing" — the difference
	// between the steady state and a deletion.
	WriterZone bool
}

// Lost reports whether a mission's lines are unaccounted for.
//
// Absence alone is not loss. The live zone is per-checkout by design
// (DES-058), so the live file is absent in every checkout but the one that
// wrote it — while a sealed chunk is git-tracked and travels to all of them.
// Reading absence as loss reported every mission a long-lived session had
// touched, in every other checkout (ethos-q6e2).
//
// But a chunk only proves the lines UP TO its watermark survived. The tail
// written after the last seal lives solely in the live file, so a chunk cannot
// vouch for a deletion in the writer's own checkout — which is precisely the
// case the chunk-derived half of the expected set was built to catch
// (docs/audit-seal.md §Seal failure policy). Suppressing on Sealed alone drops
// that whole class.
//
// WriterZone is what tells the two apart. A checkout with no live-missions zone
// never wrote these files and has nothing to say about them; a checkout that
// has the zone but not this mission's file is looking at a deletion. So a chunk
// suppresses only where the zone is absent:
//
//	!Present && !Legacy && (!Sealed || WriterZone)
//
// Bounded residual: a session that wrote in two checkouts records only one, so
// probing the recorded one can warn about a live file that is alive in the
// other. That is far smaller than dropping every deletion in a sealed mission,
// and it errs toward reporting.
func (m MissionLive) Lost() bool {
	return !m.Present && !m.Legacy && (!m.Sealed || m.WriterZone)
}

// ExpectedMissionLiveFiles returns the per-(mission, session) live-log files a
// session is expected to have written, enumerated (not globbed) so a deleted
// file surfaces at all. The expected set is the spec's union of two sources
// (docs/audit-seal.md §Seal failure policy):
//
//   - the tracked mission chunks that carry the session's id; and
//   - boundMissions, the missions the session is bound to in mission records
//     (the `ethos mission claim` sidecar and Tier B delegation records),
//     covering a session that claimed or dispatched under a mission but sealed
//     no chunk yet. The caller derives these — audit stays ignorant of the
//     record format.
//
// A file missing from disk is what a glob over extant files could never
// surface, so enumeration is what makes loss detectable at all. But absence is
// not itself loss: each entry also records whether tracked chunks or a frozen
// legacy record already hold the mission's lines, and MissionLive.Lost weighs
// the three together.
//
// Chunks and the tracked legacy log are read from trackedRoot; live files and
// the drained residue from liveRoot, the checkout that wrote them.
func ExpectedMissionLiveFiles(trackedRoot, liveRoot, sessionID string, boundMissions []string) ([]MissionLive, error) {
	seen := make(map[string]struct{})
	var ids []string
	add := func(id string) {
		id = filepath.Base(id)
		if id == "" || id == "." || id == string(filepath.Separator) {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	base := SealedMissionsBase(trackedRoot)
	missions, err := os.ReadDir(base)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", base, err)
	}
	for _, d := range missions {
		if d.IsDir() && missionChunkCarriesSession(filepath.Join(base, d.Name()), sessionID) {
			add(d.Name())
		}
	}
	for _, id := range boundMissions {
		add(id)
	}

	writerZone := liveMissionsZoneExists(liveRoot)

	sort.Strings(ids)
	out := make([]MissionLive, 0, len(ids))
	for _, id := range ids {
		livePath := LiveMissionLogPath(liveRoot, id, sessionID)
		_, statErr := os.Stat(livePath)
		sealed, err := Watermark(SealedMissionDir(trackedRoot, id), MissionNS, sessionID)
		if err != nil {
			return nil, err
		}
		legacy, err := missionIsWhollyLegacy(trackedRoot, liveRoot, id)
		if err != nil {
			return nil, err
		}
		out = append(out, MissionLive{
			MissionID:  id,
			LivePath:   livePath,
			Present:    statErr == nil,
			Sealed:     sealed > 0,
			Legacy:     legacy,
			WriterZone: writerZone,
		})
	}
	return out, nil
}

// liveMissionsZoneExists reports whether a checkout has a live-missions zone —
// evidence that mission live logs were written there, so a missing one is a
// deletion rather than the ordinary absence of another checkout's files.
//
// An empty root is not a checkout. Joining onto "" would build a relative path
// that could match some unrelated directory below the working directory, and an
// unknown writer must never be read as a present zone.
func liveMissionsZoneExists(liveRoot string) bool {
	if liveRoot == "" {
		return false
	}
	info, err := os.Stat(LiveMissionsDir(liveRoot))
	return err == nil && info.IsDir()
}

// missionIsWhollyLegacy reports whether a mission's whole record is frozen
// pre-DES-058 history: one of its frozen sources holds a parseable line and no
// session has ever sealed a chunk for it. Such a mission closed before the
// live/sealed split existed, so it has no live file to find and never will.
//
// The no-chunk condition is what keeps a frozen record from vouching for the
// wrong session. Legacy lines predate per-session attribution, so a mission
// worked both before and after the split would otherwise let its old log.jsonl
// mask a later session's genuinely lost live log. One chunk from any session
// marks a mission as post-split, and the per-session Sealed evidence takes
// over from there.
//
// The tracked log.jsonl travels with git; the drained residue does not, so each
// is read from its own root.
func missionIsWhollyLegacy(trackedRoot, liveRoot, missionID string) (bool, error) {
	if missionHasAnyChunk(SealedMissionDir(trackedRoot, missionID)) {
		return false, nil
	}
	sources := []string{
		MissionLegacyLogPath(trackedRoot, missionID),
		MissionResiduePath(liveRoot, missionID),
	}
	for _, path := range sources {
		mx, err := MaxLegacyTS(path)
		if err != nil {
			return false, err
		}
		if mx > 0 {
			return true, nil
		}
	}
	return false, nil
}

// missionHasAnyChunk reports whether dir holds a post-split chunk-namespace
// artifact for any session — the mark of a mission worked after the live/sealed
// split.
//
// Every artifact counts, not only a well-formed chunk. A mission whose chunks
// were all quarantined holds markers and retired .corrupt files and no valid
// chunk at all; reading that as "never sealed anything" would hand the mission
// back to its frozen log.jsonl and reopen the hole this predicate closes. A
// near-miss counts for the same reason — the seal fails loud on one elsewhere,
// but here it is still evidence that something was sealed. Only KindOther (a
// contract.yaml, the legacy log itself) and a stale temp are silent.
// An unreadable directory answers "maybe". Only an absent one answers "no":
// reading EACCES or EIO as "no chunks here" would let the legacy sources vouch
// for a mission whose chunk state is simply unknown, turning an I/O failure
// into a suppressed loss warning. Fail safe toward reporting.
func missionHasAnyChunk(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch _, kind := Classify(e.Name(), MissionNS); kind {
		case KindValid, KindNearMiss, KindCorrupt, KindQuarantine:
			return true
		case KindOther, KindTemp:
			// A sibling or a swept temp says nothing about sealing.
		}
	}
	return false
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
// is absent or fully sealed. Chunks come from trackedRoot, the live log from
// liveRoot — the checkout that wrote it.
func MissionUnsealedCount(trackedRoot, liveRoot, missionID, sessionID string) (int, error) {
	sealedDir := SealedMissionDir(trackedRoot, missionID)
	wm, err := Watermark(sealedDir, MissionNS, sessionID)
	if err != nil {
		return 0, err
	}
	tail, err := LiveLinesPastWatermark(LiveMissionLogPath(liveRoot, missionID, sessionID), sessionID, wm)
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
