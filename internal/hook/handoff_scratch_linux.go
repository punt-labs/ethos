package hook

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

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

func mountHandoffWritable(source, target string) error {
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	return unix.Mount("", target, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_NOSUID|unix.MS_NODEV, "")
}
