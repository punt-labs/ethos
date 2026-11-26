//go:build !linux

package hook

import (
	"context"
	"errors"
	"os"

	"github.com/punt-labs/ethos/v4/internal/checkset"
)

func handoffSandboxSupported() bool { return false }

func pinHandoffStripe(context.Context, string) (*os.File, bool, error) {
	return nil, false, errors.New("snapshot isolation unavailable on this host")
}

func withUnpinnedStripe(*os.File, func() error) (bool, error) {
	return false, errors.New("snapshot isolation unavailable on this host")
}

// RunHandoffSandbox does not dispatch a child on unsupported hosts.
func RunHandoffSandbox() {}

func runHandoffSandbox(func(string) error) {}

func handoffSandboxIdentity(string) (string, error) {
	return "", errors.New("snapshot isolation unavailable on this host")
}

func prepareHandoffSystem(context.Context, string, *checkset.WorktreeSnapshot) (handoffSystemPlan, error) {
	return handoffSystemPlan{}, nil
}

func executeHandoffSnapshot(_ context.Context, _, _, _ string, _ []string, _ editCheck, _ ...handoffSystemPlan) (string, error) {
	return "", errors.New("snapshot isolation unavailable on this host")
}
