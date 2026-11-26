//go:build darwin || linux

package hook

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var errHandoffScratchShadowsInput = errors.New("checkout scratch shadows snapshot content")

func handoffScratchTargets(checkout string, paths []string) ([]string, error) {
	var targets []string
	for _, relative := range paths {
		if relative == "." || !filepath.IsLocal(relative) || filepath.Clean(relative) != relative ||
			slices.Contains(strings.Split(relative, string(filepath.Separator)), ".git") {
			return nil, errors.New("invalid checkout scratch path")
		}
		target := filepath.Join(checkout, relative)
		if err := rejectHandoffSymlinkParents(checkout, target); err != nil {
			return nil, err
		}
		for _, other := range targets {
			if insideGatePath(target, other) || insideGatePath(other, target) {
				return nil, errors.New("overlapping checkout scratch paths")
			}
		}
		targets = append(targets, target)
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return nil, err
			}
			return nil, errHandoffScratchShadowsInput
		}
	}
	return targets, nil
}
