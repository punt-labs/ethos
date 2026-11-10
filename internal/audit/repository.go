package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// Stream identifies one monotonic writer. Legacy records do not use this
// identity: their timestamps were not necessarily unique.
type Stream struct {
	Namespace Namespace
	Session   string
	Mission   string
}

func (s Stream) Validate() error {
	for _, id := range []string{s.Session, s.Mission} {
		if id != "" && (!filepath.IsLocal(id) || filepath.Base(id) != id || bytes.ContainsAny([]byte(id), "\\\x00")) {
			return fmt.Errorf("invalid audit stream component %q", id)
		}
	}
	if s.Session == "" || s.Namespace != SessionNS && s.Namespace != MissionNS ||
		s.Namespace == MissionNS && s.Mission == "" || s.Namespace == SessionNS && s.Mission != "" {
		return fmt.Errorf("invalid audit stream %+v", s)
	}
	return nil
}

// Key has no delimiter ambiguity and is independent of mode and checkout.
func (s Stream) Key() string {
	b, _ := json.Marshal([]string{strconv.Itoa(int(s.Namespace)), s.Mission, s.Session})
	return string(b)
}

func (s Stream) LivePath(p layout.Policy) string {
	if s.Namespace == MissionNS {
		return filepath.Join(p.LocalRoot(), "missions", s.Mission, s.Session+".log.jsonl")
	}
	return filepath.Join(p.LocalRoot(), "sessions", s.Session+".audit.jsonl")
}

// WithStreamLock serializes append, archive, migration and quarantine for a
// stream across all linked checkouts. The directory check precedes lock creation.
func WithStreamLock(p layout.Policy, s Stream, fn func() error) error {
	if err := s.Validate(); err != nil {
		return err
	}
	path, err := p.LockPath("audit-stream", s.Key())
	if err != nil {
		return err
	}
	if err := layout.EnsureLocalDir(p, filepath.Dir(path)); err != nil {
		return err
	}
	return WithLock(path, fn)
}

// TryWithStreamLock is WithStreamLock without the wait: when another holder has
// the stream it returns busy=true and fn does not run.
func TryWithStreamLock(p layout.Policy, s Stream, fn func() error) (busy bool, err error) {
	if err := s.Validate(); err != nil {
		return false, err
	}
	path, err := p.LockPath("audit-stream", s.Key())
	if err != nil {
		return false, err
	}
	if err := layout.EnsureLocalDir(p, filepath.Dir(path)); err != nil {
		return false, err
	}
	return TryLock(path, fn)
}

func ArchiveRoot(p layout.Policy) string { return filepath.Join(p.LocalRoot(), "trace") }

// StreamDirs returns all historical read candidates, regardless of recording
// mode. Multiple dated directories are inspected rather than choosing the first.
func StreamDirs(p layout.Policy, s Stream) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if _, err := p.LockPath("audit-stream", s.Key()); err != nil {
		return nil, err
	}
	var dirs []string
	seen := map[string]bool{}
	for _, root := range []string{ArchiveRoot(p), p.RecordRoot(), p.CheckoutRecordRoot()} {
		if s.Namespace == MissionNS {
			dir := filepath.Join(root, "missions", s.Mission)
			if _, err := auditReadDirPresent(dir); err != nil {
				return nil, err
			}
			if !seen[dir] {
				dirs = append(dirs, dir)
				seen[dir] = true
			}
			continue
		}
		base := filepath.Join(root, "sessions")
		entries, err := readDirIfPresent(base)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !SessionDirMatches(e.Name(), s.Session) {
				continue
			}
			dir := filepath.Join(base, e.Name())
			present, err := auditReadDirPresent(dir)
			if err != nil {
				return nil, err
			}
			if present && !seen[dir] {
				dirs = append(dirs, dir)
				seen[dir] = true
			}
		}
	}
	return dirs, nil
}

// ArchiveDir reuses a session's start date from any historical layer. date is
// only used for a stream with no directory yet and must be a UTC YYYY-MM-DD.
func ArchiveDir(p layout.Policy, s Stream, date string) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.Namespace == MissionNS {
		return filepath.Join(ArchiveRoot(p), "missions", s.Mission), nil
	}
	dirs, err := StreamDirs(p, s)
	if err != nil {
		return "", err
	}
	if len(dirs) > 0 {
		date = filepath.Base(dirs[0])[:len(SessionDateFormat)]
	}
	if !SessionDirMatches(date+"-"+s.Session, s.Session) {
		return "", fmt.Errorf("invalid audit session date %q", date)
	}
	return filepath.Join(ArchiveRoot(p), "sessions", date+"-"+s.Session), nil
}

// WriteLocalAtomic stages exclusively in S's local tree, including when dest
// is a publication in C. EXDEV is returned, never worked around in the record
// tree. An existing immutable destination must contain exactly the same bytes.
func WriteLocalAtomic(p layout.Policy, dest string, data []byte) error {
	if _, err := p.LockPath("audit-stream", "validate"); err != nil {
		return err
	}
	if err := ensureDestinationDir(p, filepath.Dir(dest)); err != nil {
		return err
	}
	if info, err := os.Lstat(dest); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("audit destination %s is a symlink", dest)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if b, err := os.ReadFile(dest); err == nil {
		if bytes.Equal(b, data) {
			return syncAuditParents(p, dest)
		}
		return fmt.Errorf("audit conflict at %s: refusing to overwrite different content", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := layout.CreateLocalTemp(p, "audit", ".jsonl")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, err := writeFile(f, data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = fsyncFile(f)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return fmt.Errorf("persisting audit staging file: %w", err)
	}
	// Link is no-clobber; cooperating writers also hold the stream lock.
	if err := installLocalFile(f.Name(), dest); err != nil {
		if errors.Is(err, os.ErrExist) {
			b, rerr := os.ReadFile(dest)
			if rerr == nil && bytes.Equal(b, data) {
				return syncAuditParents(p, dest)
			}
		}
		return fmt.Errorf("installing audit file %s: %w", dest, err)
	}
	return syncAuditParents(p, dest)
}

var installLocalFile = os.Link

// ReplaceLocalFile atomically updates a mutable local collection under its
// writer's lock. It is deliberately unavailable for published files. Legacy
// migration uses this rather than staging beside a tracked audit.jsonl.
func ReplaceLocalFile(p layout.Policy, dest string, data []byte) error {
	if err := layout.EnsureLocalDir(p, filepath.Dir(dest)); err != nil {
		return err
	}
	return replaceAuditFile(p, dest, data)
}

// replaceAuditFile's caller validates the destination class before staging.
func replaceAuditFile(p layout.Policy, dest string, data []byte) error {
	if info, err := os.Lstat(dest); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("local audit collection %s is a symlink", dest)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := layout.CreateLocalTemp(p, "audit-collection", ".jsonl")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, err := writeFile(f, data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = fsyncFile(f)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), dest); err != nil {
		return fmt.Errorf("installing local audit collection (no cross-device fallback): %w", err)
	}
	return syncAuditParents(p, dest)
}

func ensureDestinationDir(p layout.Policy, dir string) error {
	if rel, err := filepath.Rel(p.LocalRoot(), dir); err == nil && (rel == "." || filepath.IsLocal(rel)) {
		return layout.EnsureLocalDir(p, dir)
	}
	// Only publication may create a non-local directory. Refuse symlinked
	// ancestors there as well; no staging file is ever created alongside it.
	allowed := false
	for _, root := range []string{p.RecordRoot(), p.CheckoutRecordRoot()} {
		rel, err := filepath.Rel(root, dir)
		allowed = allowed || err == nil && (rel == "." || filepath.IsLocal(rel))
	}
	if !allowed {
		return fmt.Errorf("audit destination outside archive/publication roots: %s", dir)
	}
	var dirs []string
	for d := dir; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("audit destination ancestor %s is not a physical directory", d)
		}
	}
	return nil
}

// StampEligibility must run after redaction and timestamp allocation. It also
// replaces caller-provided eligibility. The record field is persisted in the
// same append as its payload; stream + entry identity and canonical payload
// identify eligible immutable content, not a file or seal mode. This schema is
// shared with mission.Event. Missing legacy record is private.
func StampEligibility(p layout.Policy, s Stream, ts int64, raw []byte) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("audit payload is not an object")
	}
	var h tsHolder
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	parsed, err := ParseLineTS(h.TS)
	if err != nil || parsed != ts {
		return nil, errors.New("audit eligibility timestamp does not match payload")
	}
	if p.RecordMode() == layout.RecordFull {
		m["record"] = json.RawMessage(`"full"`)
	} else if p.RecordMode() == layout.RecordBlame {
		m["record"] = json.RawMessage(`"blame"`)
	} else {
		return nil, errors.New("invalid audit recording policy")
	}
	return json.Marshal(m)
}

func Eligible(_ Stream, l Line) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(l.Raw, &fields); err != nil {
		return false
	}
	// Eligibility is an exact protocol field, not encoding/json's permissive
	// case-insensitive struct matching. Ambiguous aliases cannot grant consent.
	for key := range fields {
		if key != "record" && strings.EqualFold(key, "record") {
			return false
		}
	}
	var record string
	return json.Unmarshal(fields["record"], &record) == nil && record == "full"
}

// MergeLines compares every duplicate before any timestamp filtering. The
// eligibility evidence is part of the payload: disagreement is a conflict too.
func MergeLines(in []Line) ([]Line, error) {
	type key struct {
		session string
		ts      int64
	}
	seen := map[key][]byte{}
	sources := map[key]string{}
	out := make([]Line, 0, len(in))
	for _, l := range in {
		b, err := CanonicalPayload(l.Raw)
		if err != nil {
			return nil, err
		}
		k := key{l.Session, l.TS}
		if old, ok := seen[k]; ok {
			if !bytes.Equal(old, b) {
				return nil, fmt.Errorf("audit identity conflict: session %q ts %d has divergent payloads in %s and %s", l.Session, l.TS, sources[k], l.Source)
			}
			continue
		}
		seen[k] = b
		sources[k] = l.Source
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

// CanonicalPayload preserves numbers and normalizes nested object key order.
func CanonicalPayload(raw []byte) ([]byte, error) {
	if _, _, duplicate := redact.DuplicateJSONKey(raw); duplicate {
		return nil, errors.New("audit payload has duplicate object keys")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("audit payload is not an object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("audit payload has trailing data")
	}
	return json.Marshal(m)
}
