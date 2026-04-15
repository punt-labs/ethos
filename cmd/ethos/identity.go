package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/session"
)

// globalStore returns the user-global identity store (~/.punt-labs/ethos).
// Exits the process on failure — acceptable at startup but not inside
// request handlers.
func globalStore() *identity.Store {
	s, err := identity.DefaultStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	return s
}

// identityStore returns a layered identity store that checks repo-local
// first, then the active bundle (if any), then user-global. Falls back
// to global-only when not inside a git repo with a .punt-labs/ethos/
// directory and no bundle is active.
func identityStore() identity.IdentityStore {
	g := globalStore()
	repoRoot := resolve.FindRepoEthosRoot()
	bundleRoot := resolveBundleRoot()
	if repoRoot == "" && bundleRoot == "" {
		return g
	}
	var repo *identity.Store
	if repoRoot != "" {
		repo = identity.NewStore(repoRoot)
	}
	var bundle *identity.Store
	if bundleRoot != "" {
		bundle = identity.NewStore(bundleRoot)
	}
	return identity.NewLayeredStoreWithBundle(repo, bundle, g)
}

// sessionStore returns the default session store rooted at the same
// location as the identity store.
func sessionStore() *session.Store {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return session.NewStore(home + "/.punt-labs/ethos")
}
