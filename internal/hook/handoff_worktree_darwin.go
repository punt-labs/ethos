//go:build darwin

package hook

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/checkset"
)

func configureDarwinWorktree(roots handoffRoots, live string, profile *checkset.WorktreeSnapshot) ([]string, []string, error) {
	if field := unsupportedWorktreeField(profile); field != "" {
		return nil, nil, fmt.Errorf("worktree profile field %s not supported by this runner yet", field)
	}
	env, err := roots.environment(profile, checkset.SnapshotPlatform{}, worktreeSandboxPATH)
	if err != nil {
		return nil, nil, err
	}
	targets, err := handoffScratchTargets(roots.checkout, profile.Scratch.Checkout)
	if err != nil {
		return nil, nil, err
	}
	for _, target := range targets {
		if err := os.MkdirAll(target, 0o700); err != nil {
			return nil, nil, err
		}
	}
	return append(env, "ETHOS_HANDOFF_CHECKOUT_NAME="+filepath.Base(live)), targets, nil
}
