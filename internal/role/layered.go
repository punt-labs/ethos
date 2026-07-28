package role

import (
	"errors"
	"fmt"
	"os"
)

// LayeredStore reads from repo-local, bundle, and user-global role stores.
// Load and Exists check repo first, then bundle, then global. List merges
// all three, deduplicating by name (repo wins, then bundle, then global).
// Save and Delete always target the global store.
//
// Under repoAuthoritative (`resolution: repo-only`, DES-057) the global
// layer is dropped from reads and writes route to the repo layer.
type LayeredStore struct {
	repo              *Store // may be nil when not in a repo
	bundle            *Store // may be nil when no bundle is active
	global            *Store
	repoAuthoritative bool
}

// NewLayeredStore creates a two-layer role store (repo + global). Kept
// as a thin wrapper over NewLayeredStoreWithBundle for callers that do
// not participate in bundle resolution.
func NewLayeredStore(repoRoot, globalRoot string) *LayeredStore {
	return NewLayeredStoreWithBundle(repoRoot, "", globalRoot, false)
}

// NewLayeredStoreWithBundle creates a three-layer role store. Any of
// repoRoot or bundleRoot may be empty; globalRoot must be set.
// repoAuthoritative selects DES-057's repo-only mode.
func NewLayeredStoreWithBundle(repoRoot, bundleRoot, globalRoot string, repoAuthoritative bool) *LayeredStore {
	var repo, bundle *Store
	if repoRoot != "" {
		repo = NewStore(repoRoot)
	}
	if bundleRoot != "" {
		bundle = NewStore(bundleRoot)
	}
	return &LayeredStore{
		repo:              repo,
		bundle:            bundle,
		global:            NewStore(globalRoot),
		repoAuthoritative: repoAuthoritative,
	}
}

// writeStore returns the layer Save and Delete target. Under repo-only,
// writing to global would produce a record no read ever sees.
func (ls *LayeredStore) writeStore() (*Store, error) {
	if !ls.repoAuthoritative {
		return ls.global, nil
	}
	if ls.repo != nil {
		return ls.repo, nil
	}
	return nil, fmt.Errorf(
		"resolution: repo-only has no repo-local role store to write to — roles are provided by the read-only bundle; edit the bundle directly")
}

// Save writes a role to the global store (the repo store under repo-only).
func (ls *LayeredStore) Save(r *Role) error {
	s, err := ls.writeStore()
	if err != nil {
		return err
	}
	return s.Save(r)
}

// Load reads a role, checking repo, then bundle, then global. Only
// falls through on not-found; real I/O errors (permission denied,
// parse failure) are surfaced rather than masked by falling through.
func (ls *LayeredStore) Load(name string) (*Role, error) {
	if ls.repo != nil {
		r, err := ls.repo.Load(name)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("repo role layer: %w", err)
		}
	}
	if ls.bundle != nil {
		r, err := ls.bundle.Load(name)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("bundle role layer: %w", err)
		}
	}
	if ls.repoAuthoritative {
		return nil, fmt.Errorf("role %q not found in the repo layer (resolution: repo-only): %w", name, os.ErrNotExist)
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
	if ls.repoAuthoritative {
		return merged, nil
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

// Delete removes a role from the global store (the repo store under
// repo-only).
func (ls *LayeredStore) Delete(name string) error {
	s, err := ls.writeStore()
	if err != nil {
		return err
	}
	return s.Delete(name)
}

// Exists reports whether the role exists in any layer.
func (ls *LayeredStore) Exists(name string) bool {
	if ls.repo != nil && ls.repo.Exists(name) {
		return true
	}
	if ls.bundle != nil && ls.bundle.Exists(name) {
		return true
	}
	if ls.repoAuthoritative {
		return false
	}
	return ls.global.Exists(name)
}
