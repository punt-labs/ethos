package hook

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

var errHandoffScratchShadowsInput = errors.New("checkout scratch shadows snapshot content")

func mountHandoffWorktreeScratch(root, checkout string, paths []string) error {
	roots := linuxHandoffRoots()
	scratch := filepath.Join(root, roots.scratch)
	for _, relative := range []string{roots.temp, roots.scratch} {
		dir := filepath.Join(root, relative)
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		if err := mountHandoffWritable(dir, dir); err != nil {
			return err
		}
	}
	if err := os.Mkdir(filepath.Join(scratch, "home"), 0o700); err != nil {
		return err
	}
	targets, err := handoffScratchTargets(checkout, paths)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.Mkdir(target, 0o700); err != nil {
			return err
		}
		backing, err := os.MkdirTemp(scratch, layout.ScratchDirPattern(".checkout"))
		if err != nil {
			return err
		}
		if err := mountHandoffWritable(backing, target); err != nil {
			return err
		}
	}
	return nil
}

func handoffScratchTargets(checkout string, paths []string) ([]string, error) {
	var targets []string
	for _, relative := range paths {
		if relative == "." || !filepath.IsLocal(relative) || filepath.Clean(relative) != relative ||
			slices.Contains(strings.Split(relative, string(filepath.Separator)), ".git") {
			return nil, errors.New("invalid checkout scratch path")
		}
		target := filepath.Join(checkout, relative)
		if err := rejectHandoffSymlinkParents(checkout, target); err != nil {
			return nil, err
		}
		for _, other := range targets {
			if insideGatePath(target, other) || insideGatePath(other, target) {
				return nil, errors.New("overlapping checkout scratch paths")
			}
		}
		targets = append(targets, target)
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return nil, err
			}
			return nil, errHandoffScratchShadowsInput
		}
	}
	return targets, nil
}

func mountHandoffWritable(source, target string) error {
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	return unix.Mount("", target, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_NOSUID|unix.MS_NODEV, "")
}
