package main

import (
	"fmt"
	"os"

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
	repoRoot := resolve.FindRepoEthosRoot()
	bundleRoot := resolveBundleRoot()
	return newIdentityStore(repoRoot, bundleRoot, repoAuthoritativeMode(repoRoot, bundleRoot))
}

// vendorSourceStore returns a store that reads the FULL chain — repo,
// bundle, and global — regardless of the repo's resolution setting.
//
// `ethos vendor` is the one command whose job is to cross the boundary
// repo-only draws: it copies FROM global INTO the repo layer. Building it
// from identityStore() would make the tool honor the very policy it
// exists to satisfy, so once a repo set `resolution: repo-only` vendor
// could no longer see the global identities it needs to complete the
// set — and the remedy `ethos doctor` prints would be unrunnable
// (Bugbot HIGH, PR #410).
//
// Everything vendor WRITES is still verified under repo-only, by the
// completeness check it runs on its own output.
func vendorSourceStore() identity.IdentityStore {
	repoRoot := resolve.FindRepoEthosRoot()
	bundleRoot := resolveBundleRoot()
	// Resolve the mode anyway, so a misconfigured repo-only repo still
	// fails at startup rather than silently vendoring under a config
	// ethos has rejected.
	repoAuthoritativeMode(repoRoot, bundleRoot)
	return newIdentityStore(repoRoot, bundleRoot, false)
}

// newIdentityStore assembles the layered store for a given policy.
func newIdentityStore(repoRoot, bundleRoot string, repoAuthoritative bool) identity.IdentityStore {
	g := globalStore()
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
// honored. The two fatal invariants — no repo layer at all, and an
// active bundle sourced from the user's home — are checked by
// bundle.VerifyRepoOnly, the one place that logic lives so
// cmd/validate-content carries the same two guards rather than a second
// copy of them (ethos-ccjz).
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
	if err := bundle.VerifyRepoOnly(repoRoot, bundleRoot, activeBundle()); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
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
