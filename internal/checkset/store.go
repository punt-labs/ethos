package checkset

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/attribute"
)

// LayeredStore follows the entity stores' repo, active bundle, global order.
// Repo-only mode retains the bundle and drops the global layer.
type LayeredStore struct{ roots []string }

func NewLayeredStoreWithBundle(repo, bundle, global string, repoOnly bool) *LayeredStore {
	store := &LayeredStore{}
	for _, root := range []string{repo, bundle} {
		if root != "" {
			store.roots = append(store.roots, root)
		}
	}
	if !repoOnly && global != "" {
		store.roots = append(store.roots, global)
	}
	return store
}

func (s *LayeredStore) Load(slug string) (*Set, error) {
	set, _, err := s.load(slug)
	return set, err
}

func (s *LayeredStore) LoadForHandoff(slug string) (*Set, error) {
	var checks, sidecar [32]byte
	set, root, err := s.load(slug, &checks)
	if err != nil {
		return nil, err
	}
	if err := loadSnapshotSidecar(root, slug, set, &sidecar); err != nil {
		for i := range set.Checks {
			if set.Checks[i].Stage == "handoff" {
				set.Checks[i].InvalidSnapshotSidecar = true
			}
		}
	} else {
		revision := fmt.Sprintf("%x", sha256.Sum256(append(checks[:], sidecar[:]...)))
		for i := range set.Checks {
			set.Checks[i].HandoffPolicySlug = slug
			set.Checks[i].HandoffPolicyRevision = revision
		}
	}
	return set, nil
}

func (s *LayeredStore) load(slug string, revision ...*[32]byte) (*Set, string, error) {
	if err := attribute.ValidateSlug(slug); err != nil {
		return nil, "", err
	}
	for _, root := range s.roots {
		file := filepath.Join(root, "checks", slug+".yaml")
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			if _, linkErr := os.Lstat(file); linkErr == nil {
				return nil, "", fmt.Errorf("reading check set %s: %w", file, err)
			}
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("reading check set %s: %w", file, err)
		}
		set, err := Decode(data)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", file, err)
		}
		if set.Name != slug {
			return nil, "", fmt.Errorf("check set %s: name %q must match filename", file, set.Name)
		}
		if len(revision) != 0 {
			*revision[0] = sha256.Sum256(data)
		}
		return set, root, nil
	}
	return nil, "", fmt.Errorf("check set %q not found: %w", slug, os.ErrNotExist)
}

func (s *LayeredStore) List() ([]string, error) {
	seen := make(map[string]bool)
	for _, root := range s.roots {
		entries, err := os.ReadDir(filepath.Join(root, "checks"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
				seen[strings.TrimSuffix(entry.Name(), ".yaml")] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (s *LayeredStore) ValidateRef(ref string) error {
	if err := ValidateReference(ref); err != nil {
		return err
	}
	if ref == "" || ref == "none" {
		return nil
	}
	_, err := s.Load(ref)
	return err
}
