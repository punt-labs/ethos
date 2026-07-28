package team

import (
	"errors"
	"fmt"
)

// LayeredStore reads from repo-local, bundle, and user-global team stores.
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

// NewLayeredStore creates a two-layer team store (repo + global). Kept
// as a thin wrapper over NewLayeredStoreWithBundle for callers that do
// not participate in bundle resolution.
func NewLayeredStore(repoRoot, globalRoot string) *LayeredStore {
	return NewLayeredStoreWithBundle(repoRoot, "", globalRoot, false)
}

// NewLayeredStoreWithBundle creates a three-layer team store. Any of
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
// writing to global would produce a team no read ever sees.
func (ls *LayeredStore) writeStore() (*Store, error) {
	if !ls.repoAuthoritative {
		return ls.global, nil
	}
	if ls.repo != nil {
		return ls.repo, nil
	}
	return nil, fmt.Errorf(
		"resolution: repo-only has no repo-local team store to write to — teams are provided by the read-only bundle; edit the bundle directly")
}

// Save writes a team to the global store (the repo store under repo-only).
func (ls *LayeredStore) Save(t *Team, identityExists, roleExists func(string) bool) error {
	s, err := ls.writeStore()
	if err != nil {
		return err
	}
	return s.Save(t, identityExists, roleExists)
}

// Load reads a team, checking repo, then bundle, then global.
func (ls *LayeredStore) Load(name string) (*Team, error) {
	if ls.repo != nil {
		t, err := ls.repo.Load(name)
		if err == nil {
			return t, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	if ls.bundle != nil {
		t, err := ls.bundle.Load(name)
		if err == nil {
			return t, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	if ls.repoAuthoritative {
		return nil, fmt.Errorf("team %q not found in the repo layer (resolution: repo-only): %w", name, ErrNotFound)
	}
	return ls.global.Load(name)
}

// List returns team names from all three stores, deduplicated.
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

// Delete removes a team from the global store (the repo store under
// repo-only).
func (ls *LayeredStore) Delete(name string) error {
	s, err := ls.writeStore()
	if err != nil {
		return err
	}
	return s.Delete(name)
}

// Exists reports whether the team exists in any layer.
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

// FindByRepo returns all teams whose Repositories list contains repo.
// Merges results from all layers, deduplicating by name. Precedence
// when deduping: repo > bundle > global. Returns an empty (non-nil)
// slice when no teams match.
func (ls *LayeredStore) FindByRepo(repo string) ([]*Team, error) {
	seen := make(map[string]struct{})
	var merged []*Team

	if ls.repo != nil {
		found, err := ls.repo.FindByRepo(repo)
		if err != nil {
			return nil, err
		}
		for _, t := range found {
			if _, ok := seen[t.Name]; ok {
				continue
			}
			seen[t.Name] = struct{}{}
			merged = append(merged, t)
		}
	}
	if ls.bundle != nil {
		found, err := ls.bundle.FindByRepo(repo)
		if err != nil {
			return nil, err
		}
		for _, t := range found {
			if _, ok := seen[t.Name]; ok {
				continue
			}
			seen[t.Name] = struct{}{}
			merged = append(merged, t)
		}
	}
	if !ls.repoAuthoritative {
		found, err := ls.global.FindByRepo(repo)
		if err != nil {
			return nil, err
		}
		for _, t := range found {
			if _, ok := seen[t.Name]; ok {
				continue
			}
			seen[t.Name] = struct{}{}
			merged = append(merged, t)
		}
	}
	if merged == nil {
		merged = []*Team{}
	}
	return merged, nil
}

// AddMember adds a member to a team. Repo- and bundle-layer teams are
// read-only.
func (ls *LayeredStore) AddMember(teamName string, m Member, identityExists, roleExists func(string) bool) error {
	if err := ls.checkNotReadOnlyLayer(teamName); err != nil {
		return err
	}
	s, err := ls.writeStore()
	if err != nil {
		return err
	}
	return s.AddMember(teamName, m, identityExists, roleExists)
}

// RemoveMember removes a member from a team. Repo- and bundle-layer
// teams are read-only.
func (ls *LayeredStore) RemoveMember(teamName, identity, role string) error {
	if err := ls.checkNotReadOnlyLayer(teamName); err != nil {
		return err
	}
	s, err := ls.writeStore()
	if err != nil {
		return err
	}
	return s.RemoveMember(teamName, identity, role)
}

// AddCollaboration adds a collaboration to a team. Repo- and bundle-layer
// teams are read-only.
func (ls *LayeredStore) AddCollaboration(teamName string, c Collaboration) error {
	if err := ls.checkNotReadOnlyLayer(teamName); err != nil {
		return err
	}
	s, err := ls.writeStore()
	if err != nil {
		return err
	}
	return s.AddCollaboration(teamName, c)
}

// checkNotReadOnlyLayer returns an error if the team exists in the repo
// or bundle layer. Both layers are read-only via CLI/MCP; the error
// message distinguishes them so the user knows where to edit. (Named for
// the layer, not for DES-057's `resolution: repo-only` mode — the two are
// unrelated.)
func (ls *LayeredStore) checkNotReadOnlyLayer(teamName string) error {
	if ls.repo != nil && ls.repo.Exists(teamName) {
		return fmt.Errorf("team %q is repo-tracked (git-tracked) and cannot be modified via CLI; edit the YAML directly", teamName)
	}
	if ls.bundle != nil && ls.bundle.Exists(teamName) {
		return fmt.Errorf("team %q is bundle-only and cannot be modified via CLI; edit the bundle directly", teamName)
	}
	return nil
}
