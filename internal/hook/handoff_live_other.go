//go:build !darwin

package hook

import (
	"context"
	"errors"
)

func armHandoffLiveGate(context.Context, string, []string) (handoffLiveGuard, string) {
	return nil, "live-verified requires Darwin"
}

func executeHandoffLive(context.Context, string, string, []string, editCheck, handoffWorktreePlan) (string, error) {
	return "", errors.New("live-verified requires Darwin")
}
