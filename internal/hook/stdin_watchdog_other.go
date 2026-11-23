//go:build !unix

package hook

import (
	"context"
	"time"
)

func StdinWatchdog(context.Context, int, time.Duration) {}
