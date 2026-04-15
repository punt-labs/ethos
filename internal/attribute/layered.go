package attribute

// NewLayeredStore creates an attribute store that reads from both repo
// and global roots. The repo root is checked first for Exists and Load.
// List merges results from both, deduplicating by slug (repo wins).
// Writes (Save, Delete) go to the global store.
// If repoRoot is empty, returns a plain global store.
//
// Kept as a thin wrapper over NewLayeredStoreWithBundle for callers
// that do not participate in bundle resolution.
func NewLayeredStore(repoRoot, globalRoot string, kind Kind) *Store {
	return NewLayeredStoreWithBundle(repoRoot, "", globalRoot, kind)
}

// NewLayeredStoreWithBundle creates a three-layer attribute store: repo
// first, then bundle, then global. Reads check in that order. Writes
// always target the global store. Any of repoRoot or bundleRoot may be
// empty; globalRoot must be set.
//
// Precedence is implemented by chaining fallback stores:
//
//	global.fallback -> bundle
//	bundle.fallback -> repo
//
// Load on the returned store checks repo, then bundle, then global;
// Exists does the same; List merges all three with repo > bundle > global.
func NewLayeredStoreWithBundle(repoRoot, bundleRoot, globalRoot string, kind Kind) *Store {
	if repoRoot == "" && bundleRoot == "" {
		return NewStore(globalRoot, kind)
	}

	// Build the chain from the lowest-precedence end (global) upward.
	// Each store's fallback is the higher-precedence layer.
	var repo, bundle *Store
	if repoRoot != "" {
		repo = NewStore(repoRoot, kind)
	}
	if bundleRoot != "" {
		bundle = &Store{root: bundleRoot, kind: kind, fallback: repo}
	}

	top := &Store{root: globalRoot, kind: kind}
	switch {
	case bundle != nil:
		top.fallback = bundle
	case repo != nil:
		top.fallback = repo
	}
	return top
}
