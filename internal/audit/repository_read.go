package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// StreamData separates the monotonic pool from frozen legacy records. Never
// deduplicate the legacy pool by timestamp: equal timestamps were legitimate.
type StreamData struct {
	Lines  []Line
	Legacy []Line
	Gaps   []Gap
	// Floor includes quarantine's verified timestamps even when no line was
	// recovered. Appenders must mint strictly above it, not above filenames.
	Floor int64
}

// ReadStream unions local archive, both record trees, legacy and the complete
// live file. In particular, overlapping live bytes are checked for conflicts
// before a watermark can hide them. It does not consult Git or recording mode.
func ReadStream(p layout.Policy, s Stream) (StreamData, error) {
	dirs, err := StreamDirs(p, s)
	if err != nil {
		return StreamData{}, err
	}
	var data StreamData
	legacyCounts := map[string]int{}
	legacyPayloads := map[string]string{}
	for _, dir := range dirs {
		session := ""
		if s.Namespace == MissionNS {
			session = s.Session
		}
		scan, err := ScanSealedDir(dir, s.Namespace, session)
		if err != nil {
			return StreamData{}, err
		}
		entries, err := readDirIfPresent(dir)
		if err != nil {
			return StreamData{}, err
		}
		for _, e := range entries {
			cn, kind := Classify(e.Name(), s.Namespace)
			if kind != KindQuarantine || s.Namespace == MissionNS && cn.Session != s.Session {
				continue
			}
			marker, err := ReadMarker(filepath.Join(dir, e.Name()))
			if err != nil {
				return StreamData{}, fmt.Errorf("reading audit quarantine marker %s: %w", filepath.Join(dir, e.Name()), err)
			}
			if marker.Chunk != cn.Stem() {
				return StreamData{}, fmt.Errorf("audit quarantine marker %s covers a different chunk", filepath.Join(dir, e.Name()))
			}
			if marker.VerifiedLast > data.Floor {
				data.Floor = marker.VerifiedLast
			}
		}
		for _, c := range SortChunks(scan.Chunks) {
			lines, err := ReadChunkVerified(filepath.Join(dir, c.ChunkFile()), c.Last)
			if err != nil {
				return StreamData{}, err
			}
			for i := range lines {
				lines[i].Session = s.Session
				lines[i].Source = filepath.Join(dir, c.ChunkFile())
			}
			data.Lines = append(data.Lines, lines...)
		}
		gaps, err := GapMarkers(dir, s.Namespace, session)
		if err != nil {
			return StreamData{}, err
		}
		data.Gaps = append(data.Gaps, gaps...)
		name := "audit.jsonl"
		if s.Namespace == MissionNS {
			name = "log.jsonl"
		}
		legacy, err := readFrozenLegacy(filepath.Join(dir, name))
		if err != nil {
			return StreamData{}, err
		}
		// Union duplicate copies as multisets, not sets. N identical legacy
		// losses in one file survive; a mirror of that file adds no extra N.
		counts := map[string]int{}
		for _, l := range legacy {
			key, err := CheckLegacyIdentity(s.Namespace, l.Raw, legacyPayloads)
			if err != nil {
				return StreamData{}, err
			}
			counts[key]++
			if counts[key] > legacyCounts[key] {
				data.Legacy = append(data.Legacy, l)
			}
		}
		for key, n := range counts {
			if n > legacyCounts[key] {
				legacyCounts[key] = n
			}
		}
	}
	live, err := readRepositoryLive(p, s)
	if err != nil {
		return StreamData{}, err
	}
	data.Lines = append(data.Lines, live...)
	data.Lines, err = MergeLines(data.Lines)
	if err != nil {
		return StreamData{}, fmt.Errorf("stream %s: %w", s.Key(), err)
	}
	for _, l := range append(data.Lines[:len(data.Lines):len(data.Lines)], data.Legacy...) {
		if l.TS > data.Floor {
			data.Floor = l.TS
		}
	}
	return data, nil
}

// CheckLegacyIdentity records the canonical payload before any copy-count
// suppression. An input hash identifies the call, not its attribution metadata.
// The caller keeps one map per stream and still counts duplicate occurrences.
func CheckLegacyIdentity(ns Namespace, raw []byte, payloads map[string]string) (string, error) {
	key, err := LegacyIdentity(ns, raw)
	if err != nil {
		return "", err
	}
	b, err := CanonicalPayload(raw)
	if err != nil {
		return "", err
	}
	if prior, ok := payloads[key]; ok && prior != string(b) {
		return "", errors.New("legacy audit identity conflict: divergent canonical payloads")
	}
	payloads[key] = string(b)
	return key, nil
}

// Live streams have exactly one owner: the resolver-provided canonical store.
// A checkout-derived local path is not a supported legacy source. Reading one
// would hide misplaced writes until that disposable checkout is removed.
func readRepositoryLive(p layout.Policy, s Stream) ([]Line, error) {
	if present, err := auditReadDirPresent(filepath.Dir(s.LivePath(p))); err != nil || !present {
		return nil, err
	}
	lines, err := readCompleteLive(s.LivePath(p), s.Session)
	if err != nil {
		return nil, err
	}
	return MergeLines(lines)
}

// A torn live tail remains in place for the writer's reopen repair. Complete
// invalid data is not absence and must not disappear behind a sealed watermark.
func readCompleteLive(path, session string) ([]Line, error) {
	if present, err := auditReadFilePresent(path); err != nil || !present {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading live audit %s: %w", path, err)
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = b[:bytes.LastIndexByte(b, '\n')+1]
	}
	var lines []Line
	for n, raw := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		if _, err := CanonicalPayload(raw); err != nil {
			return nil, fmt.Errorf("live audit %s line %d: %w", path, n+1, err)
		}
		var h tsHolder
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil, fmt.Errorf("live audit %s line %d: %w", path, n+1, err)
		}
		ts, err := ParseLineTS(h.TS)
		if err != nil {
			return nil, fmt.Errorf("live audit %s line %d: %w", path, n+1, err)
		}
		lines = append(lines, Line{TS: ts, Raw: raw, Session: session, Source: path})
	}
	return lines, nil
}

// Match the mission ID contract in internal/mission/validate.go. Audit is below
// mission in the dependency graph. Nonstandard physical directories remain
// compatibility inputs, but arbitrary unused files are not mission directories.
var auditMissionDirName = regexp.MustCompile(`^m-\d{4}-\d{2}-\d{2}-\d{3}$`)

// ListStreams discovers both live and historical streams. A missing tree is
// empty; an unreadable one is an error, never empty success. Every recognized
// artifact in a stream directory must be a readable regular file.
func ListStreams(p layout.Policy) ([]Stream, error) {
	return listStreams(p, true)
}

// ListStreamsByName discovers streams from directory and file names alone. It
// does not validate the artifacts inside a stream directory, so a caller that
// classifies them itself (the repair steps, through ScanSealedDir) is not
// stopped by one it is about to classify.
func ListStreamsByName(p layout.Policy) ([]Stream, error) {
	return listStreams(p, false)
}

func listStreams(p layout.Policy, validate bool) ([]Stream, error) {
	if _, err := p.LockPath("audit-stream", "validate"); err != nil {
		return nil, err
	}
	seen := map[Stream]bool{}
	add := func(s Stream) error {
		if err := s.Validate(); err != nil {
			return err
		}
		seen[s] = true
		return nil
	}
	roots := []string{ArchiveRoot(p), p.RecordRoot(), p.CheckoutRecordRoot()}
	for _, root := range roots {
		entries, err := readDirIfPresent(filepath.Join(root, "sessions"))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			const prefix = len("2006-01-02-")
			if len(e.Name()) > prefix && SessionDirMatches(e.Name(), e.Name()[prefix:]) {
				present, err := auditReadDirPresent(filepath.Join(root, "sessions", e.Name()))
				if err != nil {
					return nil, err
				}
				if !present {
					continue
				}
				if validate {
					if _, err := readAuditArtifactDir(filepath.Join(root, "sessions", e.Name()), SessionNS, ""); err != nil {
						return nil, err
					}
				}
				if err := add(Stream{Namespace: SessionNS, Session: e.Name()[prefix:]}); err != nil {
					return nil, err
				}
			}
		}
		missions, err := readDirIfPresent(filepath.Join(root, "missions"))
		if err != nil {
			return nil, err
		}
		for _, m := range missions {
			if !m.IsDir() && !auditMissionDirName.MatchString(m.Name()) {
				continue
			}
			var entries []os.DirEntry
			var err error
			if validate {
				entries, err = readAuditArtifactDir(filepath.Join(root, "missions", m.Name()), MissionNS, "")
			} else {
				entries, err = readDirIfPresent(filepath.Join(root, "missions", m.Name()))
			}
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if e.Name() == "log.jsonl" {
					if err := add(Stream{Namespace: MissionNS, Mission: m.Name(), Session: "no-session"}); err != nil {
						return nil, err
					}
				}
				cn, kind := Classify(e.Name(), MissionNS)
				if kind == KindNearMiss {
					return nil, fmt.Errorf("malformed chunk name %q in mission %s", e.Name(), m.Name())
				}
				if cn.Session != "" {
					if err := add(Stream{Namespace: MissionNS, Mission: m.Name(), Session: cn.Session}); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	localRoots := []string{p.LocalRoot()}
	for _, root := range localRoots {
		entries, err := readDirIfPresent(filepath.Join(root, "sessions"))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".audit.jsonl") {
				if _, err := auditReadFilePresent(filepath.Join(root, "sessions", e.Name())); err != nil {
					return nil, err
				}
				if err := add(Stream{Namespace: SessionNS, Session: strings.TrimSuffix(e.Name(), ".audit.jsonl")}); err != nil {
					return nil, err
				}
			}
		}
		missions, err := readDirIfPresent(filepath.Join(root, "missions"))
		if err != nil {
			return nil, err
		}
		for _, m := range missions {
			if !m.IsDir() && !auditMissionDirName.MatchString(m.Name()) {
				continue
			}
			dir := filepath.Join(root, "missions", m.Name())
			present, err := auditReadDirPresent(dir)
			if err != nil {
				return nil, err
			}
			if !present {
				continue
			}
			ids, err := ListLiveLogSessions(dir)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				if err := add(Stream{Namespace: MissionNS, Mission: m.Name(), Session: id}); err != nil {
					return nil, err
				}
			}
		}
	}
	out := make([]Stream, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}

// LegacyIdentity retains the pre-monotonic identity rules. Session audit tool
// calls with a hash key on ts/tool/hash, not just ts. Hashless losses use their
// canonical full payload. Mission legacy events use their full payload too.
func LegacyIdentity(ns Namespace, raw []byte) (string, error) {
	canonical, err := CanonicalPayload(raw)
	if err != nil {
		return "", err
	}
	if ns == SessionNS {
		var h struct {
			TS   string `json:"ts"`
			Tool string `json:"tool"`
			Hash string `json:"tool_input_hash"`
		}
		if err := json.Unmarshal(raw, &h); err != nil {
			return "", err
		}
		if h.Hash != "" {
			key, err := json.Marshal([]string{h.TS, h.Tool, h.Hash})
			return "hashed:" + string(key), err
		}
	}
	return "raw:" + string(canonical), nil
}

func readFrozenLegacy(path string) ([]Line, error) {
	if present, err := auditReadFilePresent(path); err != nil || !present {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading legacy audit %s: %w", path, err)
	}
	var lines []Line
	for i, raw := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		if _, err := CanonicalPayload(raw); err != nil {
			return nil, fmt.Errorf("legacy audit %s line %d: %w", path, i+1, err)
		}
		var h tsHolder
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil, fmt.Errorf("legacy audit %s line %d: %w", path, i+1, err)
		}
		// Loss markers may not carry a usable timestamp. Retain them in the
		// frozen pool, which never takes part in monotonic deduplication.
		ts, _ := ParseLineTS(h.TS)
		lines = append(lines, Line{TS: ts, Raw: raw, Source: path})
	}
	return lines, nil
}

func readDirIfPresent(dir string) ([]os.DirEntry, error) {
	if present, err := auditReadDirPresent(dir); err != nil || !present {
		return nil, err
	}
	e, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	return e, nil
}

// readAuditArtifactDir validates recognized leaves before any consumer reads a
// covering marker or filters on entry type. Legacy files are shared by every
// session; other-session chunks/markers/retirements are not selected inputs.
// Siblings, temporary files and malformed names retain their existing handling.
func readAuditArtifactDir(dir string, ns Namespace, session string) ([]os.DirEntry, error) {
	entries, err := readDirIfPresent(dir)
	if err != nil {
		return nil, err
	}
	legacy := "audit.jsonl"
	if ns == MissionNS {
		legacy = "log.jsonl"
	}
	for _, e := range entries {
		cn, kind := Classify(e.Name(), ns)
		if e.Name() != legacy {
			if kind != KindValid && kind != KindQuarantine && kind != KindCorrupt && kind != KindHeldLoss {
				continue
			}
			if ns == MissionNS && session != "" && cn.Session != session {
				continue
			}
		}
		path := filepath.Join(dir, e.Name())
		if _, err := auditReadFilePresent(path); err != nil {
			if kind == KindValid {
				return nil, &ChunkReadError{Path: path, Err: err}
			}
			return nil, err
		}
	}
	return entries, nil
}

// NotRegularFileError is a symlink, directory or special file where an audit
// artifact belongs. It is a property of the tree, not a passing condition.
type NotRegularFileError struct{ Path string }

func (e *NotRegularFileError) Error() string {
	return fmt.Sprintf("audit read file %s is not a physical regular file", e.Path)
}

// auditReadFilePresent accepts only a readable physical regular file. Lstat
// distinguishes a genuinely missing leaf from a dangling link before an open
// could turn the latter into absence. Opening read-only also checks permission
// when discovery needs no payload (notably a marker or covered retirement).
// As with the directory check, this is not an untrusted concurrent-replacement
// sandbox. No directories, locks or other files are created here.
func auditReadFilePresent(path string) (bool, error) {
	if present, err := auditReadDirPresent(filepath.Dir(path)); err != nil || !present {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("examining audit read file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, &NotRegularFileError{Path: path}
	}
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("opening audit read file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("closing audit read file %s: %w", path, err)
	}
	return true, nil
}

// auditReadDirPresent checks from the filesystem root down so Lstat never
// traverses an unchecked ancestor. Only absence is empty; a symlink (including
// a dangling one) or another non-directory type names an unreadable source.
// Unlike writer preparation this never creates directories or lock files.
// This is not a sandbox against concurrent, untrusted ancestor replacement;
// cooperating writers retain their existing locks and atomic installations.
func auditReadDirPresent(dir string) (bool, error) {
	var ancestors []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		ancestors = append(ancestors, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		d := ancestors[i]
		info, err := os.Lstat(d)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("examining audit read directory %s: %w", d, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, fmt.Errorf("audit read directory %s is not a physical directory", d)
		}
	}
	return true, nil
}

// UnarchivedCount is a purge/disable probe. Shared local chunks, not the
// invoking checkout's published watermark, prove that a live line was retained.
// The policy-taking caller must use the same snapshot as its other operations.
func UnarchivedCount(p layout.Policy, s Stream) (int, error) {
	data, err := ReadStream(p, s)
	if err != nil {
		return 0, err
	}
	archived := map[int64]bool{}
	dirs, err := StreamDirs(p, s)
	if err != nil {
		return 0, err
	}
	for _, dir := range dirs {
		rel, err := filepath.Rel(ArchiveRoot(p), dir)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		session := ""
		if s.Namespace == MissionNS {
			session = s.Session
		}
		scan, err := ScanSealedDir(dir, s.Namespace, session)
		if err != nil {
			return 0, err
		}
		for _, c := range scan.Chunks {
			lines, err := ReadChunkVerified(filepath.Join(dir, c.ChunkFile()), c.Last)
			if err != nil {
				return 0, err
			}
			for _, l := range lines {
				archived[l.TS] = true
			}
		}
	}
	count := 0
	for _, l := range data.Lines {
		if !archived[l.TS] {
			count++
		}
	}
	return count, nil
}
