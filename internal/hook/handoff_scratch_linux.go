package hook

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func prepareHandoffWorktreeScratch(root string) error {
	roots := linuxHandoffRoots()
	scratch := filepath.Join(root, roots.scratch)
	if err := os.Mkdir(filepath.Join(root, "var"), 0o700); err != nil {
		return err
	}
	for _, relative := range []string{roots.temp, roots.scratch, "/var/tmp"} {
		dir := filepath.Join(root, relative)
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		if err := mountHandoffWritable(dir, dir); err != nil {
			return err
		}
	}
	return os.Mkdir(filepath.Join(scratch, "home"), 0o700)
}

func prepareHandoffCheckoutScratch(targets []string) error {
	for _, target := range targets {
		if err := os.MkdirAll(target, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func mountHandoffCheckoutScratch(root string, targets []string) error {
	scratch := filepath.Join(root, linuxHandoffRoots().scratch)
	for _, target := range targets {
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
