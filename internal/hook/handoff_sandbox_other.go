//go:build !linux && !darwin

package hook

import (
	"context"
	"errors"
	"os"

	"github.com/punt-labs/ethos/v4/internal/checkset"
)

var errHandoffSandboxUnavailable = errors.New("snapshot isolation unavailable on this host")

func handoffSandboxSupported() bool { return false }

func pinHandoffStripe(context.Context, string) (*os.File, bool, error) {
	return nil, false, errHandoffSandboxUnavailable
}

func withUnpinnedStripe(*os.File, func() error) (bool, error) {
	return false, errHandoffSandboxUnavailable
}

// RunHandoffSandbox does not dispatch a child on unsupported hosts.
func RunHandoffSandbox() {}

func runHandoffSandbox(func(string) error) {}

func handoffSandboxIdentity(string) (string, error) {
	return "", errHandoffSandboxUnavailable
}

func prepareHandoffSystem(context.Context, string, *checkset.WorktreeSnapshot, ...string) (handoffSystemPlan, error) {
	return handoffSystemPlan{}, nil
}

func executeHandoffSnapshot(_ context.Context, _, _, _ string, _ []string, _ editCheck, _ ...handoffWorktreePlan) (string, error) {
	return "", errHandoffSandboxUnavailable
}
