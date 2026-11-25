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
	"syscall"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

const (
	appendAnchorBytes = 4 << 10
	appendSuffixBytes = 64 << 10
)

var ErrAppendDurabilityUnconfirmed = errors.New("audit entry retained; directory durability unconfirmed")

type appendFloor struct {
	Version int
	Device  uint64
	Inode   uint64
	End     int64
	Floor   int64
	Anchor  [sha256.Size]byte
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
	canCheckpoint := emptySessionDirs(p)
	if canCheckpoint {
		if cp, ok := readAppendFloor(p, s); ok {
			if used, err := appendFromFloor(p, s, cp, now, line); used {
				return err
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
	return finishSessionAppend(p, s, f, ts, canCheckpoint)
}

func emptySessionDirs(p layout.Policy) bool {
	for _, root := range []string{p.RecordRoot(), p.CheckoutRecordRoot(), ArchiveRoot(p)} {
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

func readAppendFloor(p layout.Policy, s Stream) (appendFloor, bool) {
	path := appendFloorPath(p, s)
	if present, err := auditReadDirPresent(filepath.Dir(path)); err != nil || !present {
		return appendFloor{}, false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return appendFloor{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return appendFloor{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(raw) > 1024 {
		return appendFloor{}, false
	}
	sum, body, ok := bytes.Cut(raw, []byte("\n"))
	var cp appendFloor
	if !ok || string(sum) != digest(body) || json.Unmarshal(body, &cp) != nil ||
		cp.Version != 1 || cp.End <= 0 || cp.Floor <= 0 {
		return appendFloor{}, false
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
	id, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(id.Dev) != cp.Device || uint64(id.Ino) != cp.Inode {
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
	return true, finishSessionAppend(p, s, f, ts, true)
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

func finishSessionAppend(p layout.Policy, s Stream, f *os.File, ts int64, checkpoint bool) error {
	if err := ConfirmStreamDurability(p, s); err != nil {
		return fmt.Errorf("%w: %v", ErrAppendDurabilityUnconfirmed, err)
	}
	if checkpoint {
		saveAppendFloor(p, s, f, ts)
	}
	return nil
}

func saveAppendFloor(p layout.Policy, s Stream, f *os.File, ts int64) {
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil || size <= 0 {
		return
	}
	info, err := f.Stat()
	if err != nil || info.Size() != size {
		return
	}
	id, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	anchor := make([]byte, min(size, appendAnchorBytes))
	if _, err := f.ReadAt(anchor, size-int64(len(anchor))); err != nil || anchor[len(anchor)-1] != '\n' {
		return
	}
	cp := appendFloor{Version: 1, Device: uint64(id.Dev), Inode: uint64(id.Ino), End: size, Floor: ts, Anchor: sha256.Sum256(anchor)}
	body, err := json.Marshal(cp)
	if err == nil {
		// The checkpoint is derived; a durable append does not fail if this write does.
		_ = ReplaceLocalFile(p, appendFloorPath(p, s), append([]byte(digest(body)+"\n"), body...))
	}
}
