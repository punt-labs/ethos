package hook

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// lockHeldOnThisSystem reports whether a process has the file at path open, by reading
// /proc/*/fd and comparing by inode (os.SameFile on the stat of each fd's
// target), so a path that reaches the lock through a symlink or a different
// spelling is still the same file. unknown is "" when every process could be
// checked, and otherwise says why the answer is not trustworthy: the lock could
// not be examined, /proc could not be read, or the fds of some process (another
// user's) could not be listed, so one of them might hold it.
func lockHeldOnThisSystem(path string) (held bool, unknown string) {
	want, err := os.Stat(path)
	if err != nil {
		return false, "the lock could not be examined"
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return false, "/proc could not be read"
	}
	unreadable := false
	for _, p := range procs {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) { // a process that exited is not unreadable
				unreadable = true
			}
			continue
		}
		for _, fd := range fds {
			if got, err := os.Stat(filepath.Join(fdDir, fd.Name())); err == nil && os.SameFile(want, got) {
				return true, ""
			}
		}
	}
	if unreadable {
		return false, "could not check the processes of other users"
	}
	return false, ""
}
