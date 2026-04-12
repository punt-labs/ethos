package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrArchetypeNotFound is returned when an archetype name has no YAML file.
var ErrArchetypeNotFound = errors.New("archetype not found")

// Archetype is a named set of constraints applied on top of the base
// Contract.Validate rules. Archetypes are YAML files discovered from
// the filesystem, not registered in Go code.
type Archetype struct {
	Name                string        `yaml:"name" json:"name"`
	Description         string        `yaml:"description" json:"description"`
	BudgetDefault       Budget        `yaml:"budget_default" json:"budget_default"`
	AllowEmptyWriteSet  bool          `yaml:"allow_empty_write_set" json:"allow_empty_write_set"`
	RequiredFields      []string      `yaml:"required_fields,omitempty" json:"required_fields,omitempty"`
	WriteSetConstraints []string      `yaml:"write_set_constraints,omitempty" json:"write_set_constraints,omitempty"`
}

// ArchetypeStore discovers archetype YAML files from two directories
// (repo-local and global), with repo-local overriding global.
type ArchetypeStore struct {
	repo   string // repo archetypes dir, may be empty
	global string // global archetypes dir
}

// NewArchetypeStore creates a layered archetype store. repoRoot is the
// ethos root within the repo (e.g. ".punt-labs/ethos"); globalRoot is
// the user-global ethos root (e.g. "~/.punt-labs/ethos"). Either may
// be empty, in which case that layer is skipped.
func NewArchetypeStore(repoRoot, globalRoot string) *ArchetypeStore {
	var repo, global string
	if repoRoot != "" {
		repo = filepath.Join(repoRoot, "archetypes")
	}
	if globalRoot != "" {
		global = filepath.Join(globalRoot, "archetypes")
	}
	return &ArchetypeStore{repo: repo, global: global}
}

// Load reads an archetype by name. The repo layer is checked first;
// if not found, the global layer is checked.
func (s *ArchetypeStore) Load(name string) (*Archetype, error) {
	if s.repo != "" {
		a, err := loadArchetype(s.repo, name)
		if err == nil {
			return a, nil
		}
		if !errors.Is(err, ErrArchetypeNotFound) {
			return nil, err
		}
	}
	if s.global != "" {
		return loadArchetype(s.global, name)
	}
	return nil, fmt.Errorf("archetype %q: %w", name, ErrArchetypeNotFound)
}

// List returns the names of all discovered archetypes. Repo-local
// names override global names with the same slug.
func (s *ArchetypeStore) List() ([]string, error) {
	globalNames, err := listDir(s.global)
	if err != nil {
		return nil, err
	}
	if s.repo == "" {
		return globalNames, nil
	}
	repoNames, err := listDir(s.repo)
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

// Exists reports whether an archetype with the given name exists in
// either layer.
func (s *ArchetypeStore) Exists(name string) bool {
	if s.repo != "" {
		p := filepath.Join(s.repo, name+".yaml")
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	if s.global != "" {
		p := filepath.Join(s.global, name+".yaml")
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// loadArchetype reads and parses a single archetype YAML file from dir.
func loadArchetype(dir, name string) (*Archetype, error) {
	p := filepath.Join(dir, name+".yaml")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("archetype %q: %w", name, ErrArchetypeNotFound)
		}
		return nil, fmt.Errorf("reading archetype %q: %w", name, err)
	}
	var a Archetype
	if err := yaml.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("parsing archetype %q: %w", name, err)
	}
	if a.Name == "" {
		a.Name = name
	}
	return &a, nil
}

// listDir returns archetype names from a single directory.
func listDir(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading archetypes directory %q: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	return names, nil
}
