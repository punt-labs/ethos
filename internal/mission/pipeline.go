package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrPipelineNotFound is returned when a pipeline name has no YAML file.
var ErrPipelineNotFound = errors.New("pipeline not found")

// Stage is a single step in a pipeline declaration.
type Stage struct {
	Name            string   `yaml:"name" json:"name"`
	Archetype       string   `yaml:"archetype" json:"archetype"`
	Description     string   `yaml:"description,omitempty" json:"description,omitempty"`
	Worker          string   `yaml:"worker,omitempty" json:"worker,omitempty"`
	WriteSet        []string `yaml:"write_set,omitempty" json:"write_set,omitempty"`
	InputsFrom      string   `yaml:"inputs_from,omitempty" json:"inputs_from,omitempty"`
	Evaluator       string   `yaml:"evaluator,omitempty" json:"evaluator,omitempty"`
	Budget          *Budget  `yaml:"budget,omitempty" json:"budget,omitempty"`
	SuccessCriteria []string `yaml:"success_criteria,omitempty" json:"success_criteria,omitempty"`
	Context         string   `yaml:"context,omitempty" json:"context,omitempty"`
}

// Pipeline is a named sequence of stages loaded from a YAML file.
type Pipeline struct {
	Name        string  `yaml:"name" json:"name"`
	Description string  `yaml:"description" json:"description"`
	Stages      []Stage `yaml:"stages" json:"stages"`
}

// PipelineStore discovers pipeline YAML files from two directories
// (repo-local and global), with repo-local overriding global.
type PipelineStore struct {
	repo   string // repo pipelines dir, may be empty
	global string // global pipelines dir
}

// NewPipelineStore creates a layered pipeline store. repoRoot is the
// ethos root within the repo (e.g. ".punt-labs/ethos"); globalRoot is
// the user-global ethos root (e.g. "~/.punt-labs/ethos"). Either may
// be empty, in which case that layer is skipped.
func NewPipelineStore(repoRoot, globalRoot string) *PipelineStore {
	var repo, global string
	if repoRoot != "" {
		repo = filepath.Join(repoRoot, "pipelines")
	}
	if globalRoot != "" {
		global = filepath.Join(globalRoot, "pipelines")
	}
	return &PipelineStore{repo: repo, global: global}
}

// Load reads a pipeline by name. The repo layer is checked first;
// if not found, the global layer is checked.
func (s *PipelineStore) Load(name string) (*Pipeline, error) {
	if s.repo != "" {
		p, err := loadPipeline(s.repo, name)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, ErrPipelineNotFound) {
			return nil, err
		}
	}
	if s.global != "" {
		return loadPipeline(s.global, name)
	}
	return nil, fmt.Errorf("pipeline %q: %w", name, ErrPipelineNotFound)
}

// List returns the names of all discovered pipelines. Repo-local
// names override global names with the same slug.
func (s *PipelineStore) List() ([]string, error) {
	globalNames, err := listPipelineDir(s.global)
	if err != nil {
		return nil, err
	}
	if s.repo == "" {
		return globalNames, nil
	}
	repoNames, err := listPipelineDir(s.repo)
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

// Exists reports whether a pipeline with the given name exists in
// either layer.
func (s *PipelineStore) Exists(name string) bool {
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

// loadPipeline reads and parses a single pipeline YAML file from dir.
func loadPipeline(dir, name string) (*Pipeline, error) {
	p := filepath.Join(dir, name+".yaml")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("pipeline %q: %w", name, ErrPipelineNotFound)
		}
		return nil, fmt.Errorf("reading pipeline %q: %w", name, err)
	}
	var pl Pipeline
	if err := yaml.Unmarshal(data, &pl); err != nil {
		return nil, fmt.Errorf("parsing pipeline %q: %w", name, err)
	}
	if pl.Name == "" {
		pl.Name = name
	}
	return &pl, nil
}

// listPipelineDir returns pipeline names from a single directory.
func listPipelineDir(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading pipelines directory %q: %w", dir, err)
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
