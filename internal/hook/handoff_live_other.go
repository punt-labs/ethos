//go:build !darwin

package hook

import (
	"context"
	"errors"
)

func armHandoffLive(context.Context, string, []string) (handoffLiveGuard, error) {
	return nil, errors.New("live-verified requires Darwin")
}

func executeHandoffLive(context.Context, string, string, []string, editCheck, handoffWorktreePlan) (string, error) {
	return "", errors.New("live-verified requires Darwin")
}
