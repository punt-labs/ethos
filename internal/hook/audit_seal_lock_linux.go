package hook

import (
	"os"
	"path/filepath"
	"strconv"
)

// lockHeld reports whether a process has path open, by reading /proc/*/fd.
// known is false when /proc cannot be read at all. A process of another user
// whose fds cannot be read is skipped: a git of this user is the one that
// holds this lock.
func lockHeld(path string) (held, known bool) {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return false, false
	}
	for _, p := range procs {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", p.Name(), "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if target, err := os.Readlink(filepath.Join("/proc", p.Name(), "fd", fd.Name())); err == nil && target == path {
				return true, true
			}
		}
	}
	return false, true
}
