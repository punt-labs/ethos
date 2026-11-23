package main

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStdinWatchdogCancelProbe: `--probe --cancel` fails where post-edit
// cannot stop its checks (Windows), while the plain probe still succeeds.
func TestStdinWatchdogCancelProbe(t *testing.T) {
	require.Equal(t, runtime.GOOS != "windows", postEditCancels)
	require.True(t, stdinWatchdogProbe(false, false))
	require.True(t, stdinWatchdogProbe(false, true))
	require.True(t, stdinWatchdogProbe(true, true))
	require.False(t, stdinWatchdogProbe(true, false))
}
