//go:build unix

package hook

import (
	"context"
	"os"
	"syscall"
	"time"
)

func StdinWatchdog(ctx context.Context, pid int, timeout time.Duration) {
	if pid <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		if ctx.Err() != nil {
			return
		}
		if child, err := os.FindProcess(pid); err == nil {
			defer func() { _ = child.Release() }()
			_ = child.Signal(syscall.SIGTERM)
		}
	}
}
