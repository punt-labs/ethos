//go:build darwin

package process

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// readProc returns the parent PID and command name for the given PID
// using the sysctl kern.proc.pid interface. The comm value is the
// basename of the executable path (from kern.procargs2), falling back
// to kinfo_proc P_comm when the path is unavailable.
func readProc(pid int) (ppid int, comm string, err error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, "", fmt.Errorf("sysctl kern.proc.pid.%d: %w", pid, err)
	}
	ppid = int(kp.Eproc.Ppid)

	// Try kern.procargs2 for the full executable path. This gives the
	// real binary name even when P_comm is truncated or shows a version
	// string (e.g., Claude Code reports "2.1.81" in P_comm).
	if path := execPath(pid); path != "" {
		return ppid, filepath.Base(path), nil
	}

	// Fallback: P_comm is a [16]int8 (C char array).
	commBytes := make([]byte, 0, len(kp.Proc.P_comm))
	for _, b := range kp.Proc.P_comm {
		if b == 0 {
			break
		}
		commBytes = append(commBytes, byte(b))
	}
	return ppid, strings.TrimSpace(string(commBytes)), nil
}

// execPath reads the executable path for the given PID from
// kern.procargs2. Returns empty string on any error (permissions,
// process gone, etc.).
func execPath(pid int) string {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(buf) < 4 {
		return ""
	}
	// kern.procargs2 layout: argc (4 bytes, native endian), then the
	// executable path as a NUL-terminated string.
	// Skip argc, find the first NUL to get the executable path.
	pathStart := 4
	if pathStart >= len(buf) {
		return ""
	}
	pathEnd := pathStart
	for pathEnd < len(buf) && buf[pathEnd] != 0 {
		pathEnd++
	}
	if pathEnd <= pathStart {
		return ""
	}
	return string(buf[pathStart:pathEnd])
}
