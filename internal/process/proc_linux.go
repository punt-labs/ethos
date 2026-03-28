//go:build linux

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readProc returns the parent PID and command name for the given PID
// by reading /proc/<pid>/stat.
func readProc(pid int) (ppid int, comm string, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", err
	}
	// Format: "pid (comm) state ppid ..."
	// comm can contain spaces and parentheses, so find the last ')'.
	s := string(data)
	start := strings.IndexByte(s, '(')
	end := strings.LastIndexByte(s, ')')
	if start < 0 || end < 0 || end <= start {
		return 0, "", fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	comm = s[start+1 : end]

	// Fields after ')': state ppid ...
	rest := strings.Fields(s[end+1:])
	if len(rest) < 2 {
		return 0, "", fmt.Errorf("too few fields in /proc/%d/stat", pid)
	}
	ppid, err = strconv.Atoi(rest[1])
	if err != nil {
		return 0, "", fmt.Errorf("bad ppid in /proc/%d/stat: %w", pid, err)
	}
	// /proc/pid/stat comm is the basename (max 15 chars). For Claude Code's
	// version-named binary (e.g., 2.1.86), check the symlink at /proc/pid/exe
	// for the full path containing "/claude/versions/".
	if !isClaudeComm(comm) {
		if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
			if strings.Contains(exe, "/claude/versions/") {
				comm = filepath.Base(exe)
				if !isClaudeComm(comm) {
					comm = "claude"
				}
			}
		}
	}

	return ppid, comm, nil
}
