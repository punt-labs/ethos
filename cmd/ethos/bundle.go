package main

import (
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/internal/bundle"
	"github.com/punt-labs/ethos/internal/resolve"
)

// resolveBundleRoot returns the active bundle's root path for layered
// store construction, or "" if no bundle is active or if the active
// "bundle" is the legacy .punt-labs/ethos/ directory (which already
// serves as the repo-local layer).
func resolveBundleRoot() string {
	repoRoot := resolve.FindRepoRoot()
	globalRoot := defaultGlobalRoot()
	b, err := bundle.ResolveActive(repoRoot, globalRoot)
	if err != nil || b == nil || b.Source == bundle.SourceLegacy {
		return ""
	}
	return b.Path
}

// defaultGlobalRoot returns ~/.punt-labs/ethos, matching identity.DefaultStore.
// Returns "" if the home directory cannot be determined; callers treat
// that the same as "no global bundle scope."
func defaultGlobalRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".punt-labs", "ethos")
}
