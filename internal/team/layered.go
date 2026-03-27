package team

import (
	"errors"
	"fmt"
)

// LayeredStore reads from both repo and global team stores.
// The repo store is checked first for Load and Exists.
// List merges results, deduplicating by name (repo wins).
// Save and Delete operate on the global store.
type LayeredStore struct {
	repo   *Store
	global *Store
}

// NewLayeredStore creates a layered team store. If repoRoot is empty,
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

// Save writes a team to the global store.
func (ls *LayeredStore) Save(t *Team, identityExists, roleExists func(string) bool) error {
	return ls.global.Save(t, identityExists, roleExists)
}

// Load reads a team, checking repo first then global.
func (ls *LayeredStore) Load(name string) (*Team, error) {
	if ls.repo != nil {
		t, err := ls.repo.Load(name)
		if err == nil {
			return t, nil
		}
		if errors.Is(err, ErrNotFound) {
			return ls.global.Load(name)
		}
		return nil, err
	}
	return ls.global.Load(name)
}

// List returns team names from both stores, deduplicated (repo wins).
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

// Delete removes a team from the global store.
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

// FindByRepo returns all teams whose Repositories list contains repo.
// Merges results from both layers, deduplicating by name (repo wins).
// Returns an empty (non-nil) slice when no teams match.
func (ls *LayeredStore) FindByRepo(repo string) ([]*Team, error) {
	globalTeams, err := ls.global.FindByRepo(repo)
	if err != nil {
		return nil, err
	}
	if ls.repo == nil {
		return globalTeams, nil
	}
	repoTeams, err := ls.repo.FindByRepo(repo)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(repoTeams))
	var merged []*Team
	for _, t := range repoTeams {
		seen[t.Name] = true
		merged = append(merged, t)
	}
	for _, t := range globalTeams {
		if !seen[t.Name] {
			merged = append(merged, t)
		}
	}
	if merged == nil {
		merged = []*Team{}
	}
	return merged, nil
}

// AddMember adds a member to a team. If the team is in the repo layer
// (git-tracked), returns an error — repo-layer teams are read-only.
func (ls *LayeredStore) AddMember(teamName string, m Member, identityExists, roleExists func(string) bool) error {
	if err := ls.checkNotRepoOnly(teamName); err != nil {
		return err
	}
	return ls.global.AddMember(teamName, m, identityExists, roleExists)
}

// RemoveMember removes a member from a team. Repo-layer teams are read-only.
func (ls *LayeredStore) RemoveMember(teamName, identity, role string) error {
	if err := ls.checkNotRepoOnly(teamName); err != nil {
		return err
	}
	return ls.global.RemoveMember(teamName, identity, role)
}

// AddCollaboration adds a collaboration to a team. Repo-layer teams are read-only.
func (ls *LayeredStore) AddCollaboration(teamName string, c Collaboration) error {
	if err := ls.checkNotRepoOnly(teamName); err != nil {
		return err
	}
	return ls.global.AddCollaboration(teamName, c)
}

// checkNotRepoOnly returns an error if the team exists in the repo layer.
// Repo-layer teams are git-tracked and read-only via CLI/MCP.
func (ls *LayeredStore) checkNotRepoOnly(teamName string) error {
	if ls.repo != nil && ls.repo.Exists(teamName) {
		return fmt.Errorf("team %q is repo-tracked (git-tracked) and cannot be modified via CLI; edit the YAML directly", teamName)
	}
	return nil
}
