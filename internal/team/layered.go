package team

import (
	"errors"
	"fmt"
)

// LayeredStore reads from repo-local, bundle, and user-global team stores.
// Load and Exists check repo first, then bundle, then global. List merges
// all three, deduplicating by name (repo wins, then bundle, then global).
// Save and Delete always target the global store.
type LayeredStore struct {
	repo   *Store // may be nil when not in a repo
	bundle *Store // may be nil when no bundle is active
	global *Store
}

// NewLayeredStore creates a two-layer team store (repo + global). Kept
// as a thin wrapper over NewLayeredStoreWithBundle for callers that do
// not participate in bundle resolution.
func NewLayeredStore(repoRoot, globalRoot string) *LayeredStore {
	return NewLayeredStoreWithBundle(repoRoot, "", globalRoot)
}

// NewLayeredStoreWithBundle creates a three-layer team store. Any of
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

// Save writes a team to the global store.
func (ls *LayeredStore) Save(t *Team, identityExists, roleExists func(string) bool) error {
	return ls.global.Save(t, identityExists, roleExists)
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

// Delete removes a team from the global store.
func (ls *LayeredStore) Delete(name string) error {
	return ls.global.Delete(name)
}

// Exists reports whether the team exists in any layer.
func (ls *LayeredStore) Exists(name string) bool {
	if ls.repo != nil && ls.repo.Exists(name) {
		return true
	}
	if ls.bundle != nil && ls.bundle.Exists(name) {
		return true
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
	if merged == nil {
		merged = []*Team{}
	}
	return merged, nil
}

// AddMember adds a member to a team. Repo- and bundle-layer teams are
// read-only.
func (ls *LayeredStore) AddMember(teamName string, m Member, identityExists, roleExists func(string) bool) error {
	if err := ls.checkNotRepoOnly(teamName); err != nil {
		return err
	}
	return ls.global.AddMember(teamName, m, identityExists, roleExists)
}

// RemoveMember removes a member from a team. Repo- and bundle-layer
// teams are read-only.
func (ls *LayeredStore) RemoveMember(teamName, identity, role string) error {
	if err := ls.checkNotRepoOnly(teamName); err != nil {
		return err
	}
	return ls.global.RemoveMember(teamName, identity, role)
}

// AddCollaboration adds a collaboration to a team. Repo- and bundle-layer
// teams are read-only.
func (ls *LayeredStore) AddCollaboration(teamName string, c Collaboration) error {
	if err := ls.checkNotRepoOnly(teamName); err != nil {
		return err
	}
	return ls.global.AddCollaboration(teamName, c)
}

// checkNotRepoOnly returns an error if the team exists in the repo or
// bundle layer. Both layers are read-only via CLI/MCP; the error
// message distinguishes them so the user knows where to edit.
func (ls *LayeredStore) checkNotRepoOnly(teamName string) error {
	if ls.repo != nil && ls.repo.Exists(teamName) {
		return fmt.Errorf("team %q is repo-tracked (git-tracked) and cannot be modified via CLI; edit the YAML directly", teamName)
	}
	if ls.bundle != nil && ls.bundle.Exists(teamName) {
		return fmt.Errorf("team %q is bundle-only and cannot be modified via CLI; edit the bundle directly", teamName)
	}
	return nil
}
