package hook

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
)

func waitHandoffLoad(ctx context.Context) string {
	const defaultMaxLoad = 40.0
	limit := defaultMaxLoad
	logger := diagnostic.FromContext(ctx)
	if raw := os.Getenv("ETHOS_HANDOFF_MAX_LOAD"); raw != "" {
		n, err := strconv.ParseFloat(raw, 64)
		if err == nil && n > 0 && !math.IsInf(n, 0) {
			limit = n
		} else {
			logger.Info("invalid ETHOS_HANDOFF_MAX_LOAD; using default")
		}
	}
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	waited := false
	for {
		load, err := handoffLoadAverage()
		if err != nil {
			logger.Info("handoff load unavailable", "error_type", diagnostic.ErrorType(err))
			return ""
		}
		if load <= limit {
			if waited {
				logger.Info("handoff resumed after waiting for host load", "load", load, "limit", limit)
			}
			return ""
		}
		waited = true
		select {
		case <-wait.Done():
			logger.Info("handoff deferred after waiting for host load", "load", load, "limit", limit)
			return fmt.Sprintf("handoff checks deferred after waiting: one-minute host load %.2f is above the limit of %g", load, limit)
		case <-ticker.C:
		}
	}
}
