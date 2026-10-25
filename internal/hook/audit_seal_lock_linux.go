package hook

import (
	"os"
	"path/filepath"
	"strconv"
)

// lockHeld reports whether a process has the file at path open, by reading
// /proc/*/fd and comparing by inode (os.SameFile on the stat of each fd's
// target), so a path that reaches the lock through a symlink or a different
// spelling is still the same file. known is false when the lock cannot be
// examined or /proc cannot be read. A process of another user whose fds
// cannot be read is skipped: a git of this user is the one that holds this
// lock.
func lockHeld(path string) (held, known bool) {
	want, err := os.Stat(path)
	if err != nil {
		return false, false
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return false, false
	}
	for _, p := range procs {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if got, err := os.Stat(filepath.Join(fdDir, fd.Name())); err == nil && os.SameFile(want, got) {
				return true, true
			}
		}
	}
	return false, true
}
