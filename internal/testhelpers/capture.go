// Package testhelpers provides stdio-capture helpers shared by tests
// across packages.
//
// Redirecting os.Stdout or os.Stderr to a pipe for the duration of a test
// looks simple but has a sharp edge: cleanup — restoring the original
// file, closing the pipe's write end, joining the goroutine draining it,
// and closing the read end — must run on every exit from fn, not only
// when fn returns normally. A caller's own require.NoError or t.Fatal
// inside fn is a runtime.Goexit, and a bug in fn can panic; either one
// skips straight-line cleanup code that runs only after "fn()" returns.
// The result is a leaked read descriptor and a goroutine blocked forever
// on ReadFrom, or a hang if fn produced more output than the pipe's ~64KB
// buffer holds. PR #509's review found this defect reinvented
// independently in eight separate test helpers across this repo — most
// recently fixed once in internal/hook (captureStdout in
// capture_testhelpers_test.go, captureStderr in generate_agents_test.go)
// before recurring elsewhere. This package is the one implementation, so
// the fix lives in one place instead of being re-broken a ninth time.
package testhelpers

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// CaptureStdout runs fn with os.Stdout redirected to a pipe and returns
// what was written.
func CaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	out, _ := capture(t, &os.Stdout, func() error {
		fn()
		return nil
	})
	return out
}

// CaptureStdoutE is CaptureStdout for a fn that returns an error. The
// error is asserted with require.NoError after cleanup has already run,
// so the assertion's own Goexit on failure cannot skip the pipe teardown
// the way asserting on fn's error from inside fn's own call site would.
func CaptureStdoutE(t *testing.T, fn func() error) string {
	t.Helper()
	out, err := capture(t, &os.Stdout, fn)
	require.NoError(t, err)
	return out
}

// CaptureStderr is CaptureStdout for os.Stderr.
func CaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	out, _ := capture(t, &os.Stderr, func() error {
		fn()
		return nil
	})
	return out
}

// capture redirects *target to a pipe for the duration of fn, drains the
// pipe on a separate goroutine so output larger than the OS pipe buffer
// cannot deadlock the writer, and returns the captured text alongside
// fn's error.
//
// Cleanup happens in a single deferred block: restore *target, close the
// write end (unblocking the drain goroutine), join the drain, close the
// read end — in that order, unconditionally, on every exit from fn
// including a panic or a Goexit. recover() is checked, and a panic
// re-raised, before any require assertion runs, so a caller's own
// require.NoError(t, err) inside fn and a panic in fn both leave nothing
// open.
func capture(t *testing.T, target **os.File, fn func() error) (out string, fnErr error) {
	t.Helper()

	old := *target
	r, w, err := os.Pipe()
	require.NoError(t, err)
	*target = w

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, readErr := buf.ReadFrom(r)
		done <- readErr
	}()

	defer func() {
		*target = old
		closeWriteErr := w.Close()
		drainErr := <-done
		closeReadErr := r.Close()
		out = buf.String()

		if p := recover(); p != nil {
			panic(p)
		}
		require.NoError(t, closeWriteErr)
		require.NoError(t, drainErr)
		require.NoError(t, closeReadErr)
	}()

	fnErr = fn()
	return
}
