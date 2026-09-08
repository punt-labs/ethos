package testhelpers

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// goexitHelperEnv gates TestGoexitHelperProcess so a normal `go test` run
// treats it as a no-op skip, and only the subprocess
// TestCapture_GoexitInFnDoesNotLeak spawns actually exercises it.
const goexitHelperEnv = "ETHOS_TESTHELPERS_GOEXIT_HELPER"

// goroutineCleanMarker is printed by TestGoexitHelperProcess once the
// drain goroutine it started has exited after the Goexit inside fn --
// the positive signal TestCapture_GoexitInFnDoesNotLeak looks for in the
// subprocess's output.
const goroutineCleanMarker = "GOEXIT_HELPER_GOROUTINE_CLEAN"

// TestCapture_GoexitInFnDoesNotLeak is the regression guard for PR #509's
// J2 finding: fn calling t.Fatal or a require.* assertion (both
// runtime.Goexit, not a panic) must not skip capture's cleanup and leave
// the drain goroutine blocked on ReadFrom forever.
//
// This property cannot be checked in-process in THIS test function
// without permanently failing this package's own suite: testing.T's
// FailNow-family methods must be called from the goroutine running the
// test they belong to, so triggering one requires either a raw
// runtime.Goexit() on this test's own goroutine -- which the testing
// package itself reports as a failure ("test executed panic(nil) or
// runtime.Goexit"), not something recoverable -- or a subtest via t.Run,
// whose failure propagates to every ancestor T including this one. Either
// way this test would end up permanently red.
//
// So the actual Goexit trigger and the goroutine-count check both run
// inside a subprocess (TestGoexitHelperProcess), where a subtest is
// allowed to fail: the subprocess's own exit status is not what this test
// asserts on. What is asserted is the marker TestGoexitHelperProcess
// prints only after runtime.NumGoroutine() has dropped back to its
// baseline post-Goexit -- i.e., only after the drain goroutine this
// package's capture() started has actually exited. Checking for prompt
// process exit, rather than this marker, is not equivalent: os.Exit at
// the end of a single-test subprocess run tears down every goroutine
// regardless of whether one leaked internally, which would mask exactly
// the defect this test exists to catch (confirmed while developing this
// test: a subprocess that never closes the pipe's write end still exits
// promptly, because process exit forcibly reclaims the leaked goroutine
// and its blocked read -- so "did the subprocess exit quickly" is not a
// valid proxy for "did capture's cleanup run").
//
// Falsified pre-fix: reverting capture's cleanup to run only in the
// straight-line path after "fnErr = fn()" returns -- the shape every one
// of the eight independently-duplicated helpers PR #509's review found
// had -- makes TestGoexitHelperProcess never print the marker: the drain
// goroutine started by capture() stays blocked on buf.ReadFrom(r)
// because nothing ever closes the pipe's write end when fn exits via
// Goexit instead of a normal return, so runtime.NumGoroutine() never
// drops back to baseline and the subprocess's own poll loop times out.
func TestCapture_GoexitInFnDoesNotLeak(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGoexitHelperProcess$", "-test.v") //nolint:gosec // re-invoking this package's own test binary, a standard Go subprocess-test idiom (cf. os/exec's TestHelperProcess).
	cmd.Env = append(os.Environ(), goexitHelperEnv+"=1")

	out, runErr := cmd.CombinedOutput()
	// runErr is expected to be a non-nil *exec.ExitError: the subtest
	// TestGoexitHelperProcess runs deliberately fails via require.NoError
	// on a manufactured error, which fails that subtest and therefore its
	// parent. The exit status is not what is under test here; ignore it
	// and check the marker instead.
	_ = runErr

	require.True(t, strings.Contains(string(out), goroutineCleanMarker),
		"drain goroutine leaked after Goexit inside fn -- subprocess output:\n%s", out)
}

// TestGoexitHelperProcess is not a real test of this package: it is the
// subprocess entry point TestCapture_GoexitInFnDoesNotLeak spawns. A
// direct `go test` run leaves goexitHelperEnv unset, so it skips
// immediately and contributes nothing to a normal run of this suite.
func TestGoexitHelperProcess(t *testing.T) {
	if os.Getenv(goexitHelperEnv) != "1" {
		t.Skip("subprocess entry point for TestCapture_GoexitInFnDoesNotLeak")
	}

	baseline := runtime.NumGoroutine()

	// This subtest is EXPECTED to fail -- it deliberately triggers Goexit
	// inside fn via require.NoError on a manufactured error, to exercise
	// capture's cleanup-on-abnormal-exit path. Its own pass/fail is not
	// what TestCapture_GoexitInFnDoesNotLeak checks; only whether the
	// drain goroutine capture() started is actually gone afterward is.
	t.Run("trigger", func(t *testing.T) {
		CaptureStdout(t, func() {
			require.NoError(t, errors.New("deliberate failure to exercise the Goexit path"))
		})
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if runtime.NumGoroutine() <= baseline {
			fmt.Println(goroutineCleanMarker)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("drain goroutine leaked after Goexit inside fn (goroutines: %d, baseline: %d)",
				runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
