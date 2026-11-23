//go:build unix

package hook

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStdinWatchdog(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancelled"}[cancel], func(t *testing.T) {
			child := exec.Command("sleep", "30")
			require.NoError(t, child.Start())
			t.Cleanup(func() { _ = child.Process.Kill() })
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			if cancel {
				stop()
			}
			StdinWatchdog(ctx, child.Process.Pid, 10*time.Millisecond)
			if cancel {
				require.NoError(t, child.Process.Signal(syscall.Signal(0)))
				require.NoError(t, child.Process.Kill())
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(time.Second):
				t.Fatal("watchdog did not terminate the child")
			}
		})
	}
	StdinWatchdog(context.Background(), 0, time.Hour)
	StdinWatchdog(context.Background(), -1, time.Hour)
	StdinWatchdog(context.Background(), 1<<30, 0)
}
