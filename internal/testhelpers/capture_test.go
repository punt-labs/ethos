package testhelpers

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureStdout_CapturesOutput(t *testing.T) {
	out := CaptureStdout(t, func() { fmt.Print("hello") })
	assert.Equal(t, "hello", out)
}

func TestCaptureStdoutE_CapturesOutput(t *testing.T) {
	out := CaptureStdoutE(t, func() error {
		fmt.Print("hello")
		return nil
	})
	assert.Equal(t, "hello", out)
}

func TestCaptureStderr_CapturesOutput(t *testing.T) {
	out := CaptureStderr(t, func() { fmt.Fprint(os.Stderr, "hello") })
	assert.Equal(t, "hello", out)
}

// countOpenFDs returns the number of open file descriptors for the
// current process via /dev/fd (present on Linux and macOS, the two
// platforms this suite runs tests on). Skips rather than fails if /dev/fd
// is unavailable, since this is a portability probe, not the property
// under test.
func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("cannot count open file descriptors on this platform: %v", err)
	}
	return len(entries)
}

// TestCapture_DrainGoroutineExitsOnFnError guards against the drain
// goroutine blocking on ReadFrom forever when fn returns an error rather
// than panicking or calling Goexit. Exercises the unexported capture
// directly (white-box) so the returned error can be inspected without
// require.NoError inside this test itself invoking FailNow.
func TestCapture_DrainGoroutineExitsOnFnError(t *testing.T) {
	baseline := runtime.NumGoroutine()

	out, err := capture(t, &os.Stdout, func() error {
		return errors.New("boom")
	})

	assert.Equal(t, "", out)
	require.EqualError(t, err, "boom")

	// Poll by hand rather than require.Eventually: Eventually runs the
	// condition from its own background goroutine, which is itself
	// counted by runtime.NumGoroutine() and would permanently offset the
	// comparison against a baseline taken before it started.
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

// TestCapture_PanicStillClosesReadEndAndJoinsDrain guards the panic path:
// a panic inside fn must still restore *target, close both pipe ends, and
// join the drain goroutine before capture's deferred cleanup re-panics.
func TestCapture_PanicStillClosesReadEndAndJoinsDrain(t *testing.T) {
	baseline := countOpenFDs(t)

	func() {
		defer func() {
			p := recover()
			require.Equal(t, "boom", p)
		}()
		_, _ = capture(t, &os.Stdout, func() error {
			panic("boom")
		})
	}()

	got := countOpenFDs(t)
	assert.Equal(t, baseline, got, "read end of the capture pipe leaked across a panic in fn")
}
