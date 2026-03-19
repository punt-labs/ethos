//go:build !windows

// Package process provides utilities for walking the process tree.
package process

import (
	"fmt"
	"os"
	"os/exec"
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
func FindClaudePID() string {
	claudePIDOnce.Do(func() {
		tree, err := parseProcessTree()
		if err != nil {
			claudePID = strconv.Itoa(os.Getppid())
			return
		}
		claudePID = walkToClaudeAncestor(os.Getpid(), tree)
	})
	return claudePID
}

// proc holds the parent PID and command name for a process.
type proc struct {
	ppid int
	comm string
}

// parseProcessTree runs `ps -eo pid=,ppid=,comm=` and returns a map
// from PID to proc.
func parseProcessTree() (map[int]proc, error) {
	out, err := exec.Command("ps", "-eo", "pid=,ppid=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePS(string(out))
}

// parsePS parses the output of `ps -eo pid=,ppid=,comm=` into a map.
func parsePS(output string) (map[int]proc, error) {
	tree := make(map[int]proc)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		// comm may contain spaces (e.g. "Google Chrome"); use only the
		// base name (first field after ppid) for matching.
		comm := fields[2]
		tree[pid] = proc{ppid: ppid, comm: comm}
	}
	if len(tree) == 0 {
		return nil, fmt.Errorf("no processes parsed")
	}
	return tree, nil
}

// walkToClaudeAncestor walks the process tree from startPID upward,
// returning the topmost ancestor whose basename is "claude". If none
// is found, returns the string form of os.Getppid().
func walkToClaudeAncestor(startPID int, tree map[int]proc) string {
	bestClaude := ""
	pid := startPID
	for i := 0; i < maxWalkDepth; i++ {
		p, ok := tree[pid]
		if !ok {
			break
		}
		if isClaudeComm(p.comm) {
			bestClaude = strconv.Itoa(pid)
		}
		if p.ppid == 0 || p.ppid == pid {
			break
		}
		pid = p.ppid
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
