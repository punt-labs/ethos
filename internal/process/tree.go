//go:build linux || darwin

// Package process provides utilities for walking the process tree
// using native OS interfaces (no subprocess spawning).
package process

import (
	"os"
	"strconv"
	"strings"
)

// maxWalkDepth caps the number of levels walked to avoid infinite loops
// in malformed process trees.
const maxWalkDepth = 10

// FindClaudePID identifies the PID of the owning Claude Code process for
// this call, resolved fresh on every call — never cached. A long-lived
// process (ethos serve) outlives any single session it has observed; a
// process-lifetime cache (the sync.Once this replaced) would keep
// answering with the first session's PID forever (DES-074).
//
// Prefers CLAUDE_PID, the env var Claude Code sets on every spawned
// subprocess (2.1.234+): distinct per session and per nesting level, unlike
// the topmost-ancestor walk below, which every concurrent Claude Code
// session on a host shares via the "claude daemon run" process — the root
// cause of ethos-vqwn. CLAUDE_PID is captured once at this process's own
// spawn time and never re-observed, so it is corroborated — not trusted
// outright — against the caller's CURRENT live ancestry before use: a dead
// ancestor's PID, later recycled by an unrelated but legitimate claude
// session, would otherwise resolve to a real, live, WRONG process. Falls
// back to walkToClaudeAncestor when the env var is absent (headless, CI,
// SDK, or a Claude Code version predating it) or fails corroboration.
//
// Uses native OS interfaces: /proc on Linux, sysctl on macOS.
func FindClaudePID() string {
	if pid, ok := claudePIDFromEnv(); ok && isLiveAncestor(pid) {
		return strconv.Itoa(pid)
	}
	return walkToClaudeAncestor(os.Getpid())
}

// claudePIDFromEnv parses CLAUDE_PID, returning ok=false when the variable
// is absent, blank, or not a positive integer.
func claudePIDFromEnv() (pid int, ok bool) {
	v := strings.TrimSpace(os.Getenv("CLAUDE_PID"))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// isLiveAncestor reports whether pid is currently an ancestor of the
// calling process, re-derived from the OS on every call — the
// corroboration DES-074 requires, ported from biff DES-058's
// is_live_ancestor. CLAUDE_PID is captured once at spawn and never
// re-observed, so without this check a long-lived process whose real
// ancestor died, with that PID since recycled by an unrelated but
// legitimate claude session, would resolve to a real, live, WRONG process
// merely because /proc/<pid> (or its macOS equivalent) still resolves to
// something.
func isLiveAncestor(pid int) bool {
	cur := os.Getpid()
	for i := 0; i < maxWalkDepth; i++ {
		ppid, _, err := readProc(cur)
		if err != nil {
			return false
		}
		if ppid == pid {
			return true
		}
		if ppid == 0 || ppid == cur {
			return false
		}
		cur = ppid
	}
	return false
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
