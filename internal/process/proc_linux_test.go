//go:build linux

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadProc_Self_MatchesOS(t *testing.T) {
	pid := os.Getpid()
	ppid, comm, err := readProc(pid)
	require.NoError(t, err)
	assert.Equal(t, os.Getppid(), ppid, "ppid should match os.Getppid()")
	assert.NotEmpty(t, comm, "comm should not be empty")
}

func TestReadProc_Self_CommMatchesProcStat(t *testing.T) {
	pid := os.Getpid()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	require.NoError(t, err)

	// Extract comm from raw stat — between first '(' and last ')'.
	s := string(data)
	start := strings.IndexByte(s, '(')
	end := strings.LastIndexByte(s, ')')
	require.True(t, start >= 0 && end > start, "malformed /proc/self/stat")
	rawComm := s[start+1 : end]

	_, comm, err := readProc(pid)
	require.NoError(t, err)

	// readProc may normalize via /proc/pid/exe, but if no normalization
	// applies (we're not claude), comm should match the raw value.
	exe, exeErr := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if exeErr == nil {
		expected := normalizeClaudeComm(rawComm, exe)
		assert.Equal(t, expected, comm)
	} else {
		assert.Equal(t, rawComm, comm)
	}
}

func TestReadProc_Parent(t *testing.T) {
	ppid := os.Getppid()
	grandppid, comm, err := readProc(ppid)
	require.NoError(t, err)
	assert.Greater(t, grandppid, 0, "grandparent PID should be positive")
	assert.NotEmpty(t, comm)
}

func TestReadProc_InitPpidZero(t *testing.T) {
	ppid, comm, err := readProc(1)
	if err != nil {
		t.Skipf("cannot read PID 1 (container or permission issue): %v", err)
	}
	assert.Equal(t, 0, ppid, "init ppid should be 0")
	assert.NotEmpty(t, comm)
}

func TestReadProc_NonexistentPID(t *testing.T) {
	_, _, err := readProc(999999999)
	assert.Error(t, err)
}

func TestReadProc_NegativePID(t *testing.T) {
	_, _, err := readProc(-1)
	assert.Error(t, err)
}

func TestReadProc_ExeNormalization(t *testing.T) {
	// Verify that readProc reads /proc/pid/exe and passes it to
	// normalizeClaudeComm. For our own process, the exe should be
	// readable and the comm should reflect normalization.
	pid := os.Getpid()
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	require.NoError(t, err, "/proc/self/exe should be readable")
	assert.True(t, filepath.IsAbs(exe), "exe path should be absolute: %s", exe)
}

func TestReadProc_CommTruncation(t *testing.T) {
	// Linux truncates /proc/pid/stat comm to 15 characters.
	// Spawn a child with a long name and verify readProc handles it.
	longName := "abcdefghijklmnopqrstuvwxyz" // 26 chars
	tmpDir := t.TempDir()
	longBin := filepath.Join(tmpDir, longName)

	// Create a symlink to sleep so we get a process with a long name.
	sleepPath, err := exec.LookPath("sleep")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(sleepPath, longBin))

	cmd := exec.Command(longBin, "60")
	require.NoError(t, cmd.Start())
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	childPID := cmd.Process.Pid
	ppid, comm, err := readProc(childPID)
	require.NoError(t, err)

	assert.Equal(t, os.Getpid(), ppid, "child's ppid should be our pid")
	// Linux kernel truncates comm to 15 chars in /proc/pid/stat.
	assert.Equal(t, longName[:15], comm,
		"comm should be truncated to 15 chars; got %q", comm)
}

func TestReadProc_CommWithSpaces(t *testing.T) {
	// comm field in /proc/pid/stat can contain spaces.
	// Create a binary with a space in its name.
	tmpDir := t.TempDir()
	spaceName := "my process"
	spaceBin := filepath.Join(tmpDir, spaceName)

	sleepPath, err := exec.LookPath("sleep")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(sleepPath, spaceBin))

	cmd := exec.Command(spaceBin, "60")
	require.NoError(t, cmd.Start())
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	childPID := cmd.Process.Pid
	ppid, comm, err := readProc(childPID)
	require.NoError(t, err)

	assert.Equal(t, os.Getpid(), ppid)
	assert.Equal(t, spaceName, comm,
		"comm with spaces should be parsed correctly")
}

func TestReadProc_CommWithParentheses(t *testing.T) {
	// comm field can contain parentheses — parser must use LastIndexByte(')').
	tmpDir := t.TempDir()
	parenName := "a(b)c"
	parenBin := filepath.Join(tmpDir, parenName)

	sleepPath, err := exec.LookPath("sleep")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(sleepPath, parenBin))

	cmd := exec.Command(parenBin, "60")
	require.NoError(t, cmd.Start())
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	childPID := cmd.Process.Pid
	ppid, comm, err := readProc(childPID)
	require.NoError(t, err)

	assert.Equal(t, os.Getpid(), ppid)
	assert.Equal(t, parenName, comm,
		"comm with parentheses should be parsed correctly")
}

func TestReadProc_VersionNamedBinary(t *testing.T) {
	// Simulate Claude Code's version-named binary pattern:
	// /home/user/.local/share/claude/versions/2.1.91
	// The comm in /proc/pid/stat will be "2.1.91".
	// readProc should normalize this to "claude" via /proc/pid/exe.
	//
	// On Linux, /proc/pid/exe resolves symlinks to the real binary,
	// so we must copy the binary — a symlink would resolve to the
	// original path (e.g. /usr/bin/sleep) and miss the /claude/versions/ match.
	tmpDir := t.TempDir()
	versionsDir := filepath.Join(tmpDir, ".local", "share", "claude", "versions")
	require.NoError(t, os.MkdirAll(versionsDir, 0o755))

	versionBin := filepath.Join(versionsDir, "2.1.91")
	sleepPath, err := exec.LookPath("sleep")
	require.NoError(t, err)

	// Copy the binary so /proc/pid/exe points to the copy's path.
	src, err := os.ReadFile(sleepPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(versionBin, src, 0o755))

	cmd := exec.Command(versionBin, "60")
	require.NoError(t, cmd.Start())
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	childPID := cmd.Process.Pid
	ppid, comm, err := readProc(childPID)
	require.NoError(t, err)

	assert.Equal(t, os.Getpid(), ppid)
	assert.Equal(t, "claude", comm,
		"version-named binary under /claude/versions/ should normalize to 'claude'")
}

func TestReadProc_SymlinkedVersionBinary(t *testing.T) {
	// When a version-named binary is a symlink (not a copy), Linux
	// /proc/pid/exe resolves to the symlink target. The comm field
	// shows the symlink name but exe points elsewhere — normalization
	// fails because the resolved path lacks /claude/versions/.
	//
	// This documents the Linux behavior: symlinked binaries are NOT
	// normalized to "claude". Only real files at /claude/versions/ work.
	tmpDir := t.TempDir()
	versionsDir := filepath.Join(tmpDir, ".local", "share", "claude", "versions")
	require.NoError(t, os.MkdirAll(versionsDir, 0o755))

	versionBin := filepath.Join(versionsDir, "2.1.91")
	sleepPath, err := exec.LookPath("sleep")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(sleepPath, versionBin))

	cmd := exec.Command(versionBin, "60")
	require.NoError(t, cmd.Start())
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	childPID := cmd.Process.Pid
	_, comm, err := readProc(childPID)
	require.NoError(t, err)

	// Symlink resolves to /usr/bin/sleep — no /claude/versions/ in path.
	assert.Equal(t, "2.1.91", comm,
		"symlinked version binary should NOT normalize on Linux (exe resolves through symlink)")
}

func TestReadProc_ChainToParent(t *testing.T) {
	// Walk from self → parent → grandparent. Each step should
	// return a valid PID with a decreasing or different value.
	pid := os.Getpid()
	ppid, _, err := readProc(pid)
	require.NoError(t, err)
	require.Greater(t, ppid, 0)

	gppid, _, err := readProc(ppid)
	require.NoError(t, err)
	assert.Greater(t, gppid, 0, "grandparent PID should be positive")
	assert.NotEqual(t, ppid, gppid, "parent and grandparent should differ")
}

func TestReadProc_AllFieldsPresent(t *testing.T) {
	// Verify /proc/self/stat has the expected minimum structure:
	// "pid (comm) state ppid ..." — at least 4 fields after comm.
	pid := os.Getpid()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	require.NoError(t, err)

	s := string(data)
	end := strings.LastIndexByte(s, ')')
	require.True(t, end > 0)

	rest := strings.Fields(s[end+1:])
	// state, ppid, pgrp, session — at least 4 fields
	assert.GreaterOrEqual(t, len(rest), 4,
		"expected at least 4 fields after comm in /proc/pid/stat, got %d", len(rest))

	// Field 0 is state — single character
	assert.Len(t, rest[0], 1, "state field should be a single char, got %q", rest[0])

	// Field 1 is ppid — must be numeric
	_, err = strconv.Atoi(rest[1])
	assert.NoError(t, err, "ppid field should be numeric, got %q", rest[1])
}
