//go:build linux || darwin

// Package process provides utilities for walking the process tree
// using native OS interfaces (no subprocess spawning).
package process

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// maxWalkDepth caps the number of levels walked to avoid infinite loops
// in malformed process trees.
const maxWalkDepth = 10

var (
	claudePIDOnce sync.Once
	claudePID     string
)

// FindClaudePID walks the process tree from the current PID upward,
// returning the PID of the topmost ancestor whose command name is "claude".
// The result is cached for the lifetime of the process (PIDs are stable
// within a session). Falls back to os.Getppid() if no claude ancestor
// is found or the process tree cannot be read.
//
// Uses native OS interfaces: /proc on Linux, sysctl on macOS.
func FindClaudePID() string {
	claudePIDOnce.Do(func() {
		claudePID = walkToClaudeAncestor(os.Getpid())
	})
	return claudePID
}

// walkToClaudeAncestor walks from startPID upward via readProc(),
// returning the PID string of the topmost "claude" ancestor.
// Falls back to os.Getppid() if no claude ancestor is found.
func walkToClaudeAncestor(startPID int) string {
	bestClaude := ""
	pid := startPID
	for i := 0; i < maxWalkDepth; i++ {
		ppid, comm, err := readProc(pid)
		if err != nil {
			break
		}
		if isClaudeComm(comm) {
			bestClaude = strconv.Itoa(pid)
		}
		if ppid == 0 || ppid == pid {
			break
		}
		pid = ppid
	}
	if bestClaude != "" {
		return bestClaude
	}
	return strconv.Itoa(os.Getppid())
}

// isClaudeComm checks if a process command name refers to Claude.
// Matches "claude" exactly or paths ending in "/claude".
func isClaudeComm(comm string) bool {
	base := comm
	if idx := strings.LastIndex(comm, "/"); idx >= 0 {
		base = comm[idx+1:]
	}
	return base == "claude"
}

// normalizeClaudeComm normalizes the command name for Claude Code binaries
// that are version-named under a /claude/versions/ directory (e.g.,
// ~/.local/share/claude/versions/2.1.86). Returns "claude" when the
// executable path matches, otherwise returns comm unchanged.
func normalizeClaudeComm(comm, exePath string) string {
	if isClaudeComm(comm) {
		return comm
	}
	if strings.Contains(exePath, "/claude/versions/") {
		return "claude"
	}
	return comm
}
