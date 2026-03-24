package attribute

// NewLayeredStore creates an attribute store that reads from both repo
// and global roots. The repo root is checked first for Exists and Load.
// List merges results from both, deduplicating by slug (repo wins).
// Writes (Save, Delete) go to the global store.
// If repoRoot is empty, returns a plain global store.
func NewLayeredStore(repoRoot, globalRoot string, kind Kind) *Store {
	if repoRoot == "" {
		return NewStore(globalRoot, kind)
	}
	return &Store{
		root:     globalRoot,
		kind:     kind,
		fallback: NewStore(repoRoot, kind),
	}
}
