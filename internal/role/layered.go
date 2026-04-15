package role


// LayeredStore reads from repo-local, bundle, and user-global role stores.
// Load and Exists check repo first, then bundle, then global. List merges
// all three, deduplicating by name (repo wins, then bundle, then global).
// Save and Delete always target the global store.
type LayeredStore struct {
	repo   *Store // may be nil when not in a repo
	bundle *Store // may be nil when no bundle is active
	global *Store
}

// NewLayeredStore creates a two-layer role store (repo + global). Kept
// as a thin wrapper over NewLayeredStoreWithBundle for callers that do
// not participate in bundle resolution.
func NewLayeredStore(repoRoot, globalRoot string) *LayeredStore {
	return NewLayeredStoreWithBundle(repoRoot, "", globalRoot)
}

// NewLayeredStoreWithBundle creates a three-layer role store. Any of
// repoRoot or bundleRoot may be empty; globalRoot must be set.
func NewLayeredStoreWithBundle(repoRoot, bundleRoot, globalRoot string) *LayeredStore {
	var repo, bundle *Store
	if repoRoot != "" {
		repo = NewStore(repoRoot)
	}
	if bundleRoot != "" {
		bundle = NewStore(bundleRoot)
	}
	return &LayeredStore{
		repo:   repo,
		bundle: bundle,
		global: NewStore(globalRoot),
	}
}

// Save writes a role to the global store.
func (ls *LayeredStore) Save(r *Role) error {
	return ls.global.Save(r)
}

// Load reads a role, checking repo, then bundle, then global. Only
// falls through on not-found; real I/O errors are surfaced.
func (ls *LayeredStore) Load(name string) (*Role, error) {
	if ls.repo != nil {
		if ls.repo.Exists(name) {
			return ls.repo.Load(name)
		}
	}
	if ls.bundle != nil {
		if ls.bundle.Exists(name) {
			return ls.bundle.Load(name)
		}
	}
	return ls.global.Load(name)
}

// List returns role names from all three stores, deduplicated.
// Precedence when deduping: repo > bundle > global.
func (ls *LayeredStore) List() ([]string, error) {
	seen := make(map[string]struct{})
	var merged []string

	if ls.repo != nil {
		names, err := ls.repo.List()
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			merged = append(merged, n)
		}
	}
	if ls.bundle != nil {
		names, err := ls.bundle.List()
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			merged = append(merged, n)
		}
	}
	names, err := ls.global.List()
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		merged = append(merged, n)
	}
	return merged, nil
}

// Delete removes a role from the global store.
func (ls *LayeredStore) Delete(name string) error {
	return ls.global.Delete(name)
}

// Exists reports whether the role exists in any layer.
func (ls *LayeredStore) Exists(name string) bool {
	if ls.repo != nil && ls.repo.Exists(name) {
		return true
	}
	if ls.bundle != nil && ls.bundle.Exists(name) {
		return true
	}
	return ls.global.Exists(name)
}
