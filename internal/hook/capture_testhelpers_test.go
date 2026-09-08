package hook

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout redirects os.Stdout to a pipe for the duration of fn,
// drains it on a separate goroutine so output larger than the OS pipe
// buffer (~64KB) cannot deadlock the writer, and returns the captured
// text alongside fn's error.
//
// Cleanup happens in a single deferred path, mirroring
// generate_agents_test.go's captureStderr: os.Stdout is restored, the
// write end is closed (unblocking the drain goroutine), the drain is
// joined, and the read end is closed -- in that order, unconditionally,
// on every exit from fn including a panic or t.Fatal (the PR #509 round
// 8 regression this guards against: a version that closed the write end
// only after a successful fn left the goroutine blocked on ReadFrom for
// the life of the test binary whenever fn failed; the follow-up gap a
// later round found: joining the drain and closing the read end only
// ran on the non-panic path, so a panicking fn still leaked the read
// descriptor and the goroutine, even though the comment claimed every
// exit path was covered). Only after that unconditional cleanup does
// the deferred func re-panic if fn panicked, so a caller's own
// require.NoError(t, err) on fn's error -- and a panic -- are both free
// to end the test without leaving anything open.
//
// Infra failures (os.Pipe, closing the write end, the drain read,
// closing the read end) are asserted here directly -- they are never
// expected and a caller should not have to plumb a second error value
// for them. These asserts are skipped on the panic path so the original
// panic surfaces, not a require failure about a pipe that closed abnormally
// because fn already blew up. fn's error is returned, not asserted, so
// callers that want to assert success (most) and callers that want to
// inspect the error themselves (e.g. runHookForVerifier) share one
// implementation.
func captureStdout(t *testing.T, fn func() error) (out string, fnErr error) {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, readErr := buf.ReadFrom(r)
		done <- readErr
	}()

	defer func() {
		os.Stdout = oldStdout
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

// TestCaptureStdout_DrainGoroutineExitsOnFnError guards the round 8
// regression directly: it forces fn to fail and asserts the drain
// goroutine started inside captureStdout actually exits instead of
// blocking on ReadFrom forever.
//
// Falsified pre-fix: temporarily gating the `closeW()` call in
// captureStdout on fnErr == nil (the shape the earlier, success-path-only
// version had) hangs this test indefinitely -- captureStdout itself never
// returns, blocked forever on `<-done`, because nothing ever closes the
// write end on the error path (confirmed by running it under `timeout 20`
// and observing it killed rather than completing). With the unconditional
// close restored, this test passes in well under 2s.
func TestCaptureStdout_DrainGoroutineExitsOnFnError(t *testing.T) {
	baseline := runtime.NumGoroutine()

	out, fnErr := captureStdout(t, func() error {
		return errors.New("boom")
	})

	assert.Equal(t, "", out)
	require.EqualError(t, fnErr, "boom")

	// Poll by hand rather than require.Eventually: Eventually runs the
	// condition from its own background goroutine, which is itself
	// counted by runtime.NumGoroutine() and would permanently offset
	// the comparison against a baseline taken before it started.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"drain goroutine must exit after fn returns an error, not block on ReadFrom until the process exits (goroutines: %d, baseline: %d)",
				runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// countOpenFDs returns the number of open file descriptors for the
// current process via /dev/fd (present on Linux and macOS, the two
// platforms this suite actually runs tests on -- Windows has no CI test
// job, see GOOS=windows in the Makefile's cross-compile-only check).
// Skips rather than fails if /dev/fd is unavailable, since this is a
// portability probe, not the property under test.
func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("cannot count open file descriptors on this platform: %v", err)
	}
	return len(entries)
}

// TestCaptureStdout_PanicStillClosesReadEndAndJoinsDrain pins the I3
// finding: captureStdout's doc comment claimed every exit path --
// including a panic -- restores stdout, closes the read end, and joins
// the drain before returning. That was false. The write end closed on
// panic (a bare `defer closeW()` covered it), which kept the earlier
// TestCaptureStdout_DrainGoroutineExitsOnFnError passing even for a
// panicking fn since a closed write end unblocks the drain goroutine's
// ReadFrom regardless of who is watching for it to finish. But the
// join-and-close-read-end steps ran only in the straight-line code
// after fn() returned normally, so a panicking fn skipped past them
// entirely, leaking the read end of the pipe -- a real fd leak the
// comment said could not happen. captureStderr in this same package
// (generate_agents_test.go) already did all cleanup in one
// unconditional defer; that is the shape adopted here, closing the gap
// instead of just correcting the comment.
//
// Falsified pre-fix: reverting captureStdout's body to the
// sync.Once-guarded `defer closeW()` plus a non-deferred `drainErr :=
// <-done; require.NoError(t, r.Close())` tail (this PR's prior shape)
// makes this test fail -- countOpenFDs after the panic is one higher
// than baseline, because r is never closed on the panic path.
func TestCaptureStdout_PanicStillClosesReadEndAndJoinsDrain(t *testing.T) {
	baseline := countOpenFDs(t)

	func() {
		defer func() {
			p := recover()
			require.Equal(t, "boom", p)
		}()
		_, _ = captureStdout(t, func() error {
			panic("boom")
		})
	}()

	// No polling needed here the way the drain-goroutine test above
	// needs it: captureStdout's deferred cleanup joins the drain and
	// closes r synchronously, before it re-panics, so both are already
	// done by the time recover() above returns.
	got := countOpenFDs(t)
	assert.Equal(t, baseline, got, "read end of the capture pipe leaked across a panic in fn")
}
