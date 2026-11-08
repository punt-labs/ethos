package main

import (
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// ensureLocalZonesIgnored protects both the shared local owner and the invoking
// checkout using only the request's captured roots. In a linked worktree C's
// .gitignore does not protect private files retained at S.
func ensureLocalZonesIgnored(loc resolve.Located) (added bool, err error) {
	if err := loc.Err(); err != nil {
		return false, fmt.Errorf("locating repository for the local-zone ignore rule: %w", err)
	}
	if loc.Store == "" && loc.Checkout == "" {
		return false, nil
	}
	p, err := loc.Layout()
	if err != nil {
		return false, fmt.Errorf("capturing local-zone ignore policy: %w", err)
	}
	roots := []string{loc.Store}
	// Roots has already normalized physical paths; do not resolve them again.
	if loc.Store != loc.Checkout {
		roots = append(roots, loc.Checkout)
	}
	for _, root := range roots {
		changed, err := enable.EnsureLocalIgnored(root, p)
		added = added || changed
		if err != nil {
			return added, fmt.Errorf("protecting local zone in %s: %w", root, err)
		}
	}
	return added, nil
}

// ensureLocalExtIgnored maintains the shared machine-local zone exclusion for
// setup/vendor without installing hooks or changing other enablement state.
// Repository overlays are physically local; the ignore rule does not determine
// their destination. Existing legacy protections are preserved for operator review.
func ensureLocalExtIgnored(repoRoot string) (added bool, err error) {
	if repoRoot == "" {
		return false, nil
	}
	p, err := layout.NewRepository(repoRoot, repoRoot, layout.RecordBlame)
	if err != nil {
		return false, err
	}
	return enable.EnsureLocalIgnored(repoRoot, p)
}
