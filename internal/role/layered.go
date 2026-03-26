package role

// LayeredStore reads from both repo and global role stores.
// The repo store is checked first for Load and Exists.
// List merges results, deduplicating by name (repo wins).
// Save and Delete operate on the global store.
type LayeredStore struct {
	repo   *Store
	global *Store
}

// NewLayeredStore creates a layered role store. If repoRoot is empty,
// returns a store backed only by the global root.
func NewLayeredStore(repoRoot, globalRoot string) *LayeredStore {
	var repo *Store
	if repoRoot != "" {
		repo = NewStore(repoRoot)
	}
	return &LayeredStore{
		repo:   repo,
		global: NewStore(globalRoot),
	}
}

// Save writes a role to the global store.
func (ls *LayeredStore) Save(r *Role) error {
	return ls.global.Save(r)
}

// Load reads a role, checking repo first then global.
func (ls *LayeredStore) Load(name string) (*Role, error) {
	if ls.repo != nil {
		r, err := ls.repo.Load(name)
		if err == nil {
			return r, nil
		}
		// Fall through to global only on not-found.
		if !ls.repo.Exists(name) {
			return ls.global.Load(name)
		}
		// Real error from repo (permission, parse).
		return nil, err
	}
	return ls.global.Load(name)
}

// List returns role names from both stores, deduplicated (repo wins).
func (ls *LayeredStore) List() ([]string, error) {
	globalNames, err := ls.global.List()
	if err != nil {
		return nil, err
	}
	if ls.repo == nil {
		return globalNames, nil
	}
	repoNames, err := ls.repo.List()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(repoNames))
	var merged []string
	for _, n := range repoNames {
		seen[n] = true
		merged = append(merged, n)
	}
	for _, n := range globalNames {
		if !seen[n] {
			merged = append(merged, n)
		}
	}
	return merged, nil
}

// Delete removes a role from the global store.
func (ls *LayeredStore) Delete(name string) error {
	return ls.global.Delete(name)
}

// Exists checks both stores.
func (ls *LayeredStore) Exists(name string) bool {
	if ls.repo != nil && ls.repo.Exists(name) {
		return true
	}
	return ls.global.Exists(name)
}
