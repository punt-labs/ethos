package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/internal/bundle"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/punt-labs/ethos/internal/vendor"
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
//
// This is the ONE place the DES-057 `resolution` policy is read. The
// role, team, and attribute stores are derived from the returned store,
// so every layer answers from the same reading of the repo config.
func identityStore() identity.IdentityStore {
	g := globalStore()
	repoRoot := resolve.FindRepoEthosRoot()
	bundleRoot := resolveBundleRoot()
	repoAuthoritative := repoAuthoritativeMode(repoRoot, bundleRoot)
	if repoRoot == "" && bundleRoot == "" {
		return g
	}
	var repo *identity.Store
	if repoRoot != "" {
		repo = identity.NewStore(repoRoot)
	}
	var bundleStore *identity.Store
	if bundleRoot != "" {
		bundleStore = identity.NewStore(bundleRoot)
	}
	ls := identity.NewLayeredStoreWithBundle(repo, bundleStore, g, repoAuthoritative)
	if repoAuthoritative {
		ls.WithRequiredExt(requiredExt(repoRoot, bundleRoot))
	}
	return ls
}

// requiredExt reads the vendored set's .vendor.yaml and returns the ext
// base files it recorded, keyed by handle — the set repo-only checks the
// source layer against.
//
// The manifest is read from the layer identities resolve from. A
// malformed one is a warning, not a fatal: it degrades ext completeness
// to "unverifiable" (which doctor reports) rather than making every
// command in the repo unusable over a bad advisory file.
func requiredExt(repoRoot, bundleRoot string) map[string][]string {
	root := repoRoot
	if root == "" {
		root = bundleRoot
	}
	if root == "" {
		return nil
	}
	m, err := vendor.LoadManifest(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v; extension completeness cannot be verified\n", err)
		return nil
	}
	return m.RequiredExt()
}

// repoAuthoritativeMode reports whether this repo runs in repo-only mode,
// and exits with a diagnostic when repo-only is configured but cannot be
// honored. Two states are fatal rather than silently degrading to the
// global fallback — the one thing repo-only exists to remove:
//
//   - No repo layer at all (no .punt-labs/ethos/ and no repo-local or
//     legacy bundle): there is nothing to be authoritative about.
//   - The active bundle lives in the user's home (SourceGlobal): it does
//     not travel with the checkout, so a fresh clone would resolve
//     differently. Only SourceRepo and SourceLegacy bundles qualify.
//
// Exiting here matches resolveBundleRoot and globalStore: a
// misconfiguration the user asked for is reported at startup, not
// worked around.
func repoAuthoritativeMode(repoRoot, bundleRoot string) bool {
	mode, err := resolve.ResolveResolution(resolutionConfigRoot())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if mode != resolve.ResolutionRepoOnly {
		return false
	}
	if repoRoot == "" && bundleRoot == "" {
		fmt.Fprintf(os.Stderr, "ethos: resolution: repo-only is configured but this repo has no identity store — create %s or set active_bundle to a repo-local bundle\n",
			filepath.Join(".punt-labs", "ethos"))
		os.Exit(1)
	}
	if b := activeBundle(); b != nil && b.Source == bundle.SourceGlobal {
		fmt.Fprintf(os.Stderr, "ethos: resolution: repo-only cannot use the global bundle %q at %s — a global bundle does not travel with the checkout; vendor it under %s instead\n",
			b.Name, b.Path, filepath.Join(".punt-labs", "ethos-bundles"))
		os.Exit(1)
	}
	return true
}

// resolutionConfigRoot returns the tree whose .punt-labs/ethos.yaml
// carries the resolution policy.
//
// It is StoreRepoRoot, except when a set ETHOS_REPO_ROOT was REFUSED —
// StoreRepoRoot returns "" for a refused override, which would leave the
// repo's own config unread and let a repo-only repo fall back to global
// on a warning alone. Reading the mode from the override path directly
// means the refusal cannot launder itself into the silent fallback
// repo-only exists to forbid; the missing store then trips the
// no-repo-layer error above.
func resolutionConfigRoot() string {
	if root := resolve.StoreRepoRoot(); root != "" {
		return root
	}
	root, _ := resolve.RepoRootOverride()
	return root
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
