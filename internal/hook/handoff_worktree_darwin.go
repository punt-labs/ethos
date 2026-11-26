//go:build darwin

package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/punt-labs/ethos/v4/internal/checkset"
)

func configureDarwinWorktree(roots handoffRoots, live string, profile *checkset.WorktreeSnapshot) ([]string, []string, error) {
	platform := profile.System.Platforms[runtime.GOOS+"-"+runtime.GOARCH]
	hasSystem := func(inputs checkset.SnapshotSystemInputs) bool {
		return len(inputs.Tools)+len(inputs.Files)+len(inputs.Dirs) != 0
	}
	for _, feature := range []struct {
		name string
		used bool
	}{
		{"runtime extraction", profile.Runtime != nil},
		{"system inputs", hasSystem(profile.System.SnapshotSystemInputs) || hasSystem(platform)},
		{"data", len(profile.Data) != 0},
		{"scratch.seed", len(profile.Scratch.Seed) != 0},
		{"git", profile.Git != nil},
		{"network", profile.Network != ""},
		{"prehydrate", profile.Prehydrate != nil},
	} {
		if feature.used {
			return nil, nil, fmt.Errorf("worktree snapshot feature not implemented: %s", feature.name)
		}
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
