//go:build linux

package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func addBundleFlags(t *testing.T) {
	t.Helper()
	prevApply, prevGlobal, prevName := addBundleApply, addBundleGlobal, addBundleName
	addBundleApply, addBundleGlobal, addBundleName = true, true, ""
	t.Cleanup(func() { addBundleApply, addBundleGlobal, addBundleName = prevApply, prevGlobal, prevName })
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
}

func fakeGitOnPath(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nwhile [ \"$1\" = -c ]; do shift 2; done\n"+body), 0o755))
	t.Setenv("PATH", bin)
}

// git's stderr is ethos's own file, not a pipe of ours: clone progress (which git
// shows only for a terminal) reaches a pty.
func TestAddBundleProgressReachesTheTerminal(t *testing.T) {
	addBundleFlags(t)
	fakeGitOnPath(t, "if [ -t 2 ]; then echo 'Receiving objects: 100% (3/3), done.' >&2; fi\nexit 0\n")
	// Non-blocking, so the read deadline below applies: a blocking master would
	// hang the test for as long as git printed nothing.
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	defer m.Close()
	require.NoError(t, unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0))
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	require.NoError(t, err)
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	require.NoError(t, err)
	defer slave.Close()

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(slave)
	require.NoError(t, runTeamAddBundle(cmd, "https://example.invalid/o/b.git"))

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		_ = m.SetReadDeadline(time.Now().Add(3 * time.Second))
		k, _ := m.Read(buf)
		got <- string(buf[:k])
	}()
	assert.Contains(t, <-got, "Receiving objects", "progress reached the terminal")
}

// A child that keeps git's stderr open after git exits must not turn a successful
// clone into a failure (as a copying pipe would, when Wait waits for its end).
func TestAddBundleStderrHeldOpenByAChildIsNotAFailure(t *testing.T) {
	addBundleFlags(t)
	// The fake PATH holds only the fake git, so name sleep by its path: with a
	// missing sleep the background child never runs and the test proves nothing.
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	fakeGitOnPath(t, sleep+" 5 >&2 &\nexit 0\n")
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	go func() { _, _ = io.Copy(io.Discard, r) }()

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(w)
	start := time.Now()
	require.NoError(t, runTeamAddBundle(cmd, "https://example.invalid/o/b.git"))
	assert.Less(t, time.Since(start), 5*time.Second)
}
