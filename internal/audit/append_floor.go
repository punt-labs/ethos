package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

const (
	appendAnchorBytes = 4 << 10
	appendSuffixBytes = 64 << 10
	appendFloorBytes  = 8 << 10
	appendArchiveDirs = 8
)

var ErrAppendDurabilityUnconfirmed = errors.New("audit entry retained; directory durability unconfirmed")

type appendFloor struct {
	Version int
	Device  uint64
	Inode   uint64
	End     int64
	Floor   int64
	Anchor  [sha256.Size]byte
	Archive []appendDirStamp
}

type appendDirStamp struct {
	Name string
	Dev  uint64
	Ino  uint64
	Time int64
}

func appendFloorPath(p layout.Policy, s Stream) string {
	return filepath.Join(p.LocalRoot(), "cursors", "append-v1-"+digest([]byte(s.Key()))+".json")
}

// AppendSession appends under the caller's repository session stream lock.
func AppendSession(p layout.Policy, s Stream, now time.Time, line func(int64) ([]byte, error)) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Namespace != SessionNS {
		return errors.New("expected repository session audit stream")
	}
	path := s.LivePath(p)
	if err := layout.EnsureLocalDir(p, filepath.Dir(path)); err != nil {
		return err
	}
	canCheckpoint := emptyPublishedSessionDirs(p)
	if canCheckpoint {
		if cp, ok := readAppendFloor(p, s); ok {
			if sameArchiveDirs(p, cp.Archive) {
				if used, err := appendFromFloor(p, s, cp, now, line); used {
					return err
				}
			}
		}
	}
	data, err := ReadStream(p, s)
	if err != nil {
		return err
	}
	f, err := openMonotonic(path)
	if err != nil {
		return err
	}
	defer f.Close()
	ts, err := recoverAndAppend(f, data.Floor, now, line)
	if err != nil {
		return err
	}
	var archive []appendDirStamp
	if canCheckpoint {
		archive = captureArchiveDirs(p, s)
	}
	return finishSessionAppend(p, s, f, ts, archive)
}

// InvalidateAppendFloor must finish before a repository history mutation.
// The caller holds the session stream lock shared by all linked checkouts.
func InvalidateAppendFloor(p layout.Policy, s Stream) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Namespace != SessionNS {
		return nil
	}
	return ReplaceLocalFile(p, appendFloorPath(p, s), []byte("invalid\n"))
}

func emptyPublishedSessionDirs(p layout.Policy) bool {
	for i, root := range []string{p.RecordRoot(), p.CheckoutRecordRoot()} {
		if i == 1 && root == p.RecordRoot() {
			continue
		}
		dir := filepath.Join(root, "sessions")
		present, err := auditReadDirPresent(dir)
		if err != nil {
			return false
		}
		if !present {
			continue
		}
		f, err := os.Open(dir)
		if err != nil {
			return false
		}
		names, readErr := f.Readdirnames(1)
		closeErr := f.Close()
		if len(names) != 0 || readErr != io.EOF || closeErr != nil {
			return false
		}
	}
	return true
}

// Directory stamps detect inventory changes, including additions by older
// writers. They are not checks of historical chunk contents.
func captureArchiveDirs(p layout.Policy, s Stream) []appendDirStamp {
	base := filepath.Join(ArchiveRoot(p), "sessions")
	stamp, ok := statAppendDir(base, "")
	if !ok {
		return nil
	}
	stamps := []appendDirStamp{stamp}
	if stamp.Ino == 0 {
		return stamps
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !SessionDirMatches(entry.Name(), s.Session) {
			continue
		}
		if len(stamps) == appendArchiveDirs+1 {
			return nil
		}
		stamp, ok := statAppendDir(base, entry.Name())
		if !ok {
			return nil
		}
		stamps = append(stamps, stamp)
	}
	return stamps
}

func sameArchiveDirs(p layout.Policy, stamps []appendDirStamp) bool {
	base := filepath.Join(ArchiveRoot(p), "sessions")
	for _, old := range stamps {
		stamp, ok := statAppendDir(base, old.Name)
		if !ok || stamp != old {
			return false
		}
	}
	return true
}

func statAppendDir(base, name string) (appendDirStamp, bool) {
	path := filepath.Join(base, name)
	present, err := auditReadDirPresent(path)
	if err != nil || !present {
		return appendDirStamp{Name: name}, err == nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return appendDirStamp{}, false
	}
	dev, ino, ok := appendFileIdentity(info)
	if !ok || !info.IsDir() {
		return appendDirStamp{}, false
	}
	return appendDirStamp{Name: name, Dev: dev, Ino: ino, Time: info.ModTime().UnixNano()}, true
}

func readAppendFloor(p layout.Policy, s Stream) (appendFloor, bool) {
	path := appendFloorPath(p, s)
	if present, err := auditReadDirPresent(filepath.Dir(path)); err != nil || !present {
		return appendFloor{}, false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > appendFloorBytes {
		return appendFloor{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return appendFloor{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, appendFloorBytes+1))
	if err != nil || len(raw) > appendFloorBytes {
		return appendFloor{}, false
	}
	sum, body, ok := bytes.Cut(raw, []byte("\n"))
	var cp appendFloor
	if !ok || string(sum) != digest(body) || json.Unmarshal(body, &cp) != nil ||
		cp.Version != 1 || cp.End <= 0 || cp.Floor <= 0 || len(cp.Archive) == 0 ||
		len(cp.Archive) > appendArchiveDirs+1 || cp.Archive[0].Name != "" {
		return appendFloor{}, false
	}
	for _, stamp := range cp.Archive[1:] {
		if !SessionDirMatches(stamp.Name, s.Session) {
			return appendFloor{}, false
		}
	}
	return cp, true
}

func appendFromFloor(p layout.Policy, s Stream, cp appendFloor, now time.Time, line func(int64) ([]byte, error)) (bool, error) {
	path := s.LivePath(p)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return false, nil
	}
	dev, ino, ok := appendFileIdentity(info)
	if !ok || dev != cp.Device || ino != cp.Inode {
		return false, nil
	}
	end, floor, ok := checkAppendTail(f, info.Size(), cp)
	if !ok {
		return false, nil
	}
	if end < info.Size() {
		if err := f.Truncate(end); err != nil {
			return true, fmt.Errorf("truncating torn live audit tail %s: %w", path, err)
		}
	}
	ts, err := appendAtFloor(f, floor, now, line)
	if err != nil {
		return true, err
	}
	return true, finishSessionAppend(p, s, f, ts, cp.Archive)
}

func checkAppendTail(f io.ReaderAt, size int64, cp appendFloor) (int64, int64, bool) {
	if size < cp.End || size-cp.End > appendSuffixBytes {
		return 0, 0, false
	}
	anchorSize := min(cp.End, appendAnchorBytes)
	tail := make([]byte, size-cp.End+anchorSize)
	if _, err := f.ReadAt(tail, cp.End-anchorSize); err != nil {
		return 0, 0, false
	}
	anchor, suffix := tail[:anchorSize], tail[anchorSize:]
	if anchor[len(anchor)-1] != '\n' || sha256.Sum256(anchor) != cp.Anchor {
		return 0, 0, false
	}
	floor, end := cp.Floor, cp.End
	for {
		raw, rest, ok := bytes.Cut(suffix, []byte("\n"))
		if !ok {
			return end, floor, true
		}
		ts, err := chunkLineTimestamp(raw)
		if err != nil || ts <= floor {
			return 0, 0, false
		}
		if _, err := CanonicalPayload(raw); err != nil {
			return 0, 0, false
		}
		floor = ts
		end += int64(len(raw) + 1)
		suffix = rest
	}
}

func finishSessionAppend(p layout.Policy, s Stream, f *os.File, ts int64, archive []appendDirStamp) error {
	if err := ConfirmStreamDurability(p, s); err != nil {
		return fmt.Errorf("%w: %v", ErrAppendDurabilityUnconfirmed, err)
	}
	if len(archive) != 0 {
		saveAppendFloor(p, s, f, ts, archive)
	}
	return nil
}

func saveAppendFloor(p layout.Policy, s Stream, f *os.File, ts int64, archive []appendDirStamp) {
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil || size <= 0 {
		return
	}
	info, err := f.Stat()
	if err != nil || info.Size() != size {
		return
	}
	dev, ino, ok := appendFileIdentity(info)
	if !ok {
		return
	}
	anchor := make([]byte, min(size, appendAnchorBytes))
	if _, err := f.ReadAt(anchor, size-int64(len(anchor))); err != nil || anchor[len(anchor)-1] != '\n' {
		return
	}
	cp := appendFloor{Version: 1, Device: dev, Inode: ino, End: size, Floor: ts, Anchor: sha256.Sum256(anchor), Archive: archive}
	body, err := json.Marshal(cp)
	if err == nil {
		// The checkpoint is derived; a durable append does not fail if this write does.
		_ = ReplaceLocalFile(p, appendFloorPath(p, s), append([]byte(digest(body)+"\n"), body...))
	}
}
