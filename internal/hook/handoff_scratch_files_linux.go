package hook

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/punt-labs/ethos/v4/internal/redact"
)

type handoffScratchFileOverlay struct {
	parent string
	names  map[string]bool
}

const handoffScratchOverlayPhase = "scratch.files requires a usable overlayfs mount"

func handoffScratchFileOverlays(files []string) []handoffScratchFileOverlay {
	groups := make(map[string]map[string]bool)
	for _, file := range files {
		parent := filepath.Dir(file)
		if groups[parent] == nil {
			groups[parent] = make(map[string]bool)
		}
		groups[parent][filepath.Base(file)] = true
	}
	overlays := make([]handoffScratchFileOverlay, 0, len(groups))
	for parent, names := range groups {
		overlays = append(overlays, handoffScratchFileOverlay{parent, names})
	}
	sort.Slice(overlays, func(i, j int) bool {
		a, b := overlays[i].parent, overlays[j].parent
		// The checkout root must be first, and ancestors sort before descendants.
		return a == "." || b != "." && a < b
	})
	return overlays
}

func (overlay handoffScratchFileOverlay) dirs(state string, index int) (string, string) {
	dir := filepath.Join(state, fmt.Sprint(index))
	return filepath.Join(dir, "upper"), filepath.Join(dir, "work")
}

func mountHandoffScratchFiles(checkout, state string, overlays []handoffScratchFileOverlay, laterMounts []string) error {
	if len(overlays) == 0 {
		return nil
	}
	lower := filepath.Join(state, "lower")
	if err := mountGateReadOnly(checkout, lower); err != nil {
		return fmt.Errorf("bind lower checkout: %w", err)
	}
	for i, overlay := range overlays {
		upper, work := overlay.dirs(state, i)
		if err := os.Mkdir(work, 0o700); err != nil {
			return err
		}
		base := filepath.Join(lower, overlay.parent)
		if strings.ContainsAny(base+upper+work, `,:\`) {
			return errors.New("unsupported overlay path")
		}
		options := "userxattr,lowerdir=" + base + ",upperdir=" + upper + ",workdir=" + work
		target := filepath.Join(checkout, overlay.parent)
		if err := unix.Mount("overlay", target, "overlay", unix.MS_NOSUID|unix.MS_NODEV, options); err != nil {
			return fmt.Errorf("mount overlay for %q: %w", overlay.parent, err)
		}
		if err := protectHandoffOverlayEntries(base, target, overlay.names, laterMounts); err != nil {
			return err
		}
	}
	return unix.Unmount(lower, unix.MNT_DETACH)
}

func protectHandoffOverlayEntries(lower, target string, declared map[string]bool, laterMounts []string) error {
	entries, err := os.ReadDir(lower)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(target, name)
		if declared[name] || slices.Contains(laterMounts, path) || (!entry.IsDir() && !entry.Type().IsRegular()) {
			continue
		}
		if err := bindHandoffReadOnly(filepath.Join(lower, name), path); err != nil {
			return fmt.Errorf("protect overlay entry %q: %w", filepath.Base(path), err)
		}
	}
	return nil
}

type handoffScratchFileWatch struct {
	file    *os.File
	parents map[int32]handoffScratchFileOverlay
}

func watchHandoffScratchFiles(state string, overlays []handoffScratchFileOverlay) (*handoffScratchFileWatch, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	w := &handoffScratchFileWatch{os.NewFile(uintptr(fd), "handoff scratch watch"), make(map[int32]handoffScratchFileOverlay, len(overlays))}
	for i, overlay := range overlays {
		upper, _ := overlay.dirs(state, i)
		if err := os.MkdirAll(upper, 0o700); err != nil {
			_ = w.file.Close()
			return nil, err
		}
		const mask = unix.IN_CREATE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM | unix.IN_DELETE | unix.IN_ATTRIB |
			unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_DONT_FOLLOW | unix.IN_ONLYDIR
		wd, err := unix.InotifyAddWatch(fd, upper, mask)
		if err != nil {
			_ = w.file.Close()
			return nil, err
		}
		w.parents[int32(wd)] = overlay
	}
	return w, nil
}

func drainHandoffScratchEvents(fd int, visit func(int32, uint32, string) error) error {
	var buf [4096]byte
	for {
		n, err := unix.Read(fd, buf[:])
		switch err {
		case unix.EAGAIN:
			return nil
		case unix.EINTR:
			continue
		case nil:
		default:
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		for data := buf[:n]; len(data) > 0; {
			if len(data) < unix.SizeofInotifyEvent {
				return io.ErrUnexpectedEOF
			}
			wd := int32(binary.NativeEndian.Uint32(data[:4]))
			mask := binary.NativeEndian.Uint32(data[4:8])
			length := binary.NativeEndian.Uint32(data[12:16])
			data = data[unix.SizeofInotifyEvent:]
			if uint64(length) > uint64(len(data)) {
				return io.ErrUnexpectedEOF
			}
			if visit != nil {
				name, _, _ := bytes.Cut(data[:length], []byte{0})
				if err := visit(wd, mask, string(name)); err != nil {
					return err
				}
			}
			data = data[length:]
		}
	}
}

func (w *handoffScratchFileWatch) audit(state string, overlays []handoffScratchFileOverlay) error {
	violated := map[string]bool{}
	watchErr := drainHandoffScratchEvents(int(w.file.Fd()), func(wd int32, mask uint32, name string) error {
		if mask&unix.IN_Q_OVERFLOW != 0 {
			return errors.New("scratch-violation: scratch.files event queue overflow")
		}
		overlay, ok := w.parents[wd]
		if !ok {
			return errors.New("cannot audit scratch.files overlay: unexpected watch descriptor")
		}
		if !overlay.names[name] || mask&unix.IN_ISDIR != 0 {
			violated[filepath.Join(gateRoot, overlay.parent, name)] = true
		}
		return nil
	})
	if watchErr != nil {
		return watchErr
	}
	for i, overlay := range overlays {
		upper, _ := overlay.dirs(state, i)
		info, err := os.Lstat(upper)
		if err != nil {
			return errors.New("cannot audit scratch.files overlay")
		}
		if info.Mode() != os.ModeDir|0o700 || scratchFileOverlayHasXattr(upper) {
			violated[filepath.Join(gateRoot, overlay.parent)] = true
			continue
		}
		entries, err := os.ReadDir(upper)
		if err != nil {
			return errors.New("cannot audit scratch.files overlay")
		}
		for _, entry := range entries {
			if !overlay.names[entry.Name()] || !entry.Type().IsRegular() {
				violated[filepath.Join(gateRoot, overlay.parent, entry.Name())] = true
			}
		}
	}
	if len(violated) == 0 {
		return nil
	}
	violations := make([]string, 0, len(violated))
	for name := range violated {
		violations = append(violations, name)
	}
	sort.Strings(violations)
	const maxReportedPaths = 20
	var reported []string
	for _, name := range violations[:min(len(violations), maxReportedPaths)] {
		reported = append(reported, fmt.Sprintf("%q", redact.Prose(name)))
	}
	if more := len(violations) - len(reported); more > 0 {
		reported = append(reported, fmt.Sprintf("%d more", more))
	}
	return fmt.Errorf("scratch-violation: %s", strings.Join(reported, ", "))
}

func scratchFileOverlayHasXattr(path string) bool {
	size, err := unix.Llistxattr(path, nil)
	if err != nil || size == 0 {
		return err != nil
	}
	names := make([]byte, size)
	n, err := unix.Llistxattr(path, names)
	if err != nil {
		return true
	}
	// Overlayfs records a UUID at mount time and marks copy-up directories impure.
	for name := range bytes.SplitSeq(names[:n], []byte{0}) {
		switch string(name) {
		case "", "user.overlay.uuid", "user.overlay.impure":
		default:
			return true
		}
	}
	return false
}

func removeHandoffScratchFiles(state string) {
	if os.RemoveAll(state) == nil {
		return
	}
	// Overlayfs creates restricted work directories. The gate may also have
	// removed access to upper directories. Restore access without following links.
	_ = filepath.WalkDir(state, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}
		return os.Chmod(path, 0o700)
	})
	_ = os.RemoveAll(state)
}
