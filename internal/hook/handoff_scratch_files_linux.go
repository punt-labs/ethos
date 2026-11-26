package hook

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

func mountHandoffScratchFiles(checkout, state string, overlays []handoffScratchFileOverlay) error {
	if len(overlays) == 0 {
		return nil
	}
	lower := filepath.Join(state, "lower")
	if err := mountGateReadOnly(checkout, lower); err != nil {
		return fmt.Errorf("bind lower checkout: %w", err)
	}
	for i, overlay := range overlays {
		upper, work := overlay.dirs(state, i)
		for _, dir := range []string{upper, work} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
		}
		base := filepath.Join(lower, overlay.parent)
		if strings.ContainsAny(base+upper+work, `,:\`) {
			return errors.New("unsupported overlay path")
		}
		options := "userxattr,lowerdir=" + base + ",upperdir=" + upper + ",workdir=" + work
		if err := unix.Mount("overlay", filepath.Join(checkout, overlay.parent), "overlay", unix.MS_NOSUID|unix.MS_NODEV, options); err != nil {
			return fmt.Errorf("mount overlay for %q: %w", overlay.parent, err)
		}
	}
	return unix.Unmount(lower, unix.MNT_DETACH)
}

func auditHandoffScratchFiles(state string, overlays []handoffScratchFileOverlay) error {
	var violations []string
	for i, overlay := range overlays {
		upper, _ := overlay.dirs(state, i)
		info, err := os.Lstat(upper)
		if err != nil {
			return errors.New("cannot audit scratch.files overlay")
		}
		if info.Mode() != os.ModeDir|0o700 || scratchFileOverlayHasXattr(upper) {
			violations = append(violations, filepath.Join(gateRoot, overlay.parent))
			continue
		}
		entries, err := os.ReadDir(upper)
		if err != nil {
			return errors.New("cannot audit scratch.files overlay")
		}
		for _, entry := range entries {
			if !overlay.names[entry.Name()] || !entry.Type().IsRegular() {
				violations = append(violations, filepath.Join(gateRoot, overlay.parent, entry.Name()))
			}
		}
	}
	if len(violations) == 0 {
		return nil
	}
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
