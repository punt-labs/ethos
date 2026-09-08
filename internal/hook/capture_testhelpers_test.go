package hook

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"sync"
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
// The write end closes on every exit from fn -- success, a returned
// error, or a panic -- so a caller never leaks the drain goroutine or
// its file descriptors (the PR #509 round 8 regression: a version
// that closed only after a successful fn left the goroutine blocked
// on ReadFrom for the life of the test binary whenever fn failed).
// Restoring os.Stdout, closing the read end, and waiting for the
// drain to finish all happen before this function returns, so a
// caller's own require.NoError(t, err) on fn's error is free to fail
// the test without leaving anything open.
//
// Infra failures (os.Pipe, the drain read, closing the read end) are
// asserted here directly -- they are never expected and a caller
// should not have to plumb a second error value for them. fn's error
// is returned, not asserted, so callers that want to assert success
// (most) and callers that want to inspect the error themselves (e.g.
// runHookForVerifier) share one implementation.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, readErr := buf.ReadFrom(r)
		done <- readErr
	}()

	// sync.Once makes the deferred safety-net close a no-op once the
	// explicit close below has already run on the normal path.
	var closeOnce sync.Once
	closeW := func() { closeOnce.Do(func() { _ = w.Close() }) }
	defer closeW()

	fnErr := fn()
	closeW() // unblocks the reader goroutine promptly on every path

	drainErr := <-done
	require.NoError(t, r.Close())
	require.NoError(t, drainErr)

	return buf.String(), fnErr
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
