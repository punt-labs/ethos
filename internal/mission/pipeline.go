package mission

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
// Validates that the pipeline name is slug-safe and that stage names are
// unique within the pipeline.
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
	if !pipelineNamePattern.MatchString(pl.Name) {
		return nil, fmt.Errorf("pipeline %q (file %s): name is not a valid slug: must match ^[a-z0-9][a-z0-9-]*$", pl.Name, name+".yaml")
	}
	seen := make(map[string]bool, len(pl.Stages))
	for _, s := range pl.Stages {
		if seen[s.Name] {
			return nil, fmt.Errorf("pipeline %q: duplicate stage name %q", pl.Name, s.Name)
		}
		seen[s.Name] = true
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

// InstantiateOptions controls pipeline instantiation.
type InstantiateOptions struct {
	PipelineID string            // If empty, auto-generated as <name>-<YYYY-MM-DD>-<6 hex>.
	Vars       map[string]string // Template variables for {key} substitution.
	Leader     string            // Required. Sets Contract.Leader for every stage.
	Evaluator  string            // Default evaluator handle. Stage.Evaluator overrides.
	Worker     string            // Default worker handle. Stage.Worker overrides.
	Now        time.Time         // Timestamp for ID generation and contract fields.
	Archetypes *ArchetypeStore   // Optional. When set, applies archetype budget defaults.
	DryRun     bool              // When true, use synthetic IDs instead of allocating real ones.
}

// Instantiate produces one unsaved Contract per stage in the pipeline.
// Each contract has its Type set to the stage archetype, Pipeline set to
// the resolved pipeline ID, DependsOn populated from InputsFrom, and
// template variables expanded in WriteSet, Context, and SuccessCriteria.
//
// Returns the slice of unsaved contracts in stage order. The caller
// saves them via Store.Create.
func Instantiate(p *Pipeline, opts InstantiateOptions) ([]*Contract, error) {
	if p == nil {
		return nil, fmt.Errorf("pipeline is nil")
	}
	if strings.TrimSpace(opts.Leader) == "" {
		return nil, fmt.Errorf("instantiate %q: leader is required", p.Name)
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}

	pipelineID := opts.PipelineID
	if pipelineID == "" {
		id, err := generatePipelineID(p.Name, opts.Now)
		if err != nil {
			return nil, fmt.Errorf("instantiate %q: generating pipeline ID: %w", p.Name, err)
		}
		pipelineID = id
	}

	// Build stage name → index map for InputsFrom resolution.
	stageIndex := make(map[string]int, len(p.Stages))
	for i, s := range p.Stages {
		stageIndex[s.Name] = i
	}

	contracts := make([]*Contract, len(p.Stages))
	for i, stage := range p.Stages {
		// Always use synthetic placeholder IDs. Real IDs are assigned
		// later by ApplyServerFields in the CLI save loop, so Instantiate
		// never burns counter slots.
		missionID := fmt.Sprintf("m-placeholder-%03d", i+1)

		// Resolve worker: stage > opts > "".
		worker := opts.Worker
		if stage.Worker != "" {
			worker = stage.Worker
		}

		// Resolve evaluator: stage > opts > "".
		evaluator := opts.Evaluator
		if stage.Evaluator != "" {
			evaluator = stage.Evaluator
		}

		// Expand template variables in write_set, context, success_criteria.
		ws, err := expandSlice(stage.WriteSet, opts.Vars, p.Name, stage.Name, "write_set")
		if err != nil {
			return nil, err
		}
		ctx, err := ExpandVars(stage.Context, opts.Vars)
		if err != nil {
			return nil, fmt.Errorf("instantiate %q stage %q context: %w", p.Name, stage.Name, err)
		}
		sc, err := expandSlice(stage.SuccessCriteria, opts.Vars, p.Name, stage.Name, "success_criteria")
		if err != nil {
			return nil, err
		}

		// Resolve DependsOn from InputsFrom.
		var dependsOn []string
		if stage.InputsFrom != "" {
			upIdx, ok := stageIndex[stage.InputsFrom]
			if !ok {
				return nil, fmt.Errorf("instantiate %q: stage %q references unknown stage %q via inputs_from",
					p.Name, stage.Name, stage.InputsFrom)
			}
			if contracts[upIdx] == nil {
				return nil, fmt.Errorf("instantiate %q: stage %q depends on %q which has not been assigned an ID yet",
					p.Name, stage.Name, stage.InputsFrom)
			}
			dependsOn = []string{contracts[upIdx].MissionID}
		}

		// Budget: stage override > archetype default > zero (will fail
		// validation if neither is set).
		var budget Budget
		if stage.Budget != nil {
			budget = *stage.Budget
		} else if opts.Archetypes != nil {
			a, loadErr := opts.Archetypes.Load(stage.Archetype)
			if loadErr == nil {
				budget = a.BudgetDefault
			}
		}

		now := opts.Now.UTC().Format(time.RFC3339)
		c := &Contract{
			MissionID: missionID,
			Status:    StatusOpen,
			Type:      stage.Archetype,
			CreatedAt: now,
			UpdatedAt: now,
			Leader:    opts.Leader,
			Worker:    worker,
			Evaluator: Evaluator{
				Handle:   evaluator,
				PinnedAt: now,
			},
			WriteSet:        ws,
			SuccessCriteria: sc,
			Budget:          budget,
			CurrentRound:    1,
			Context:         ctx,
			Pipeline:        pipelineID,
			DependsOn:       dependsOn,
		}
		contracts[i] = c
	}
	return contracts, nil
}

// pipelineNamePattern validates that a pipeline name is a slug-safe
// value suitable for embedding in a pipeline ID. The generated ID
// concatenates the name with a date and hex suffix, so the name must
// be lowercase alphanumeric with hyphens.
var pipelineNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// generatePipelineID produces a pipeline ID of the form
// <name>-<YYYY-MM-DD>-<6 hex chars>. Returns an error if name is
// not a valid slug.
func generatePipelineID(name string, now time.Time) (string, error) {
	if !pipelineNamePattern.MatchString(name) {
		return "", fmt.Errorf("pipeline name %q is not a valid slug: must match ^[a-z0-9][a-z0-9-]*$", name)
	}
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random suffix: %w", err)
	}
	day := now.UTC().Format("2006-01-02")
	id := fmt.Sprintf("%s-%s-%x", name, day, b)
	if len(id) > 128 {
		return "", fmt.Errorf("pipeline name %q produces ID of length %d (max 128): shorten the pipeline name", name, len(id))
	}
	return id, nil
}

// ExpandVars replaces {key} tokens with their values from vars.
// Double braces escape: {{ becomes literal {, }} becomes literal }.
// An empty placeholder {} is rejected. Returns an error listing the
// first unknown or empty token encountered.
func ExpandVars(s string, vars map[string]string) (string, error) {
	var result strings.Builder
	for i := 0; i < len(s); {
		// Escaped open brace: {{ → literal {
		if i+1 < len(s) && s[i] == '{' && s[i+1] == '{' {
			result.WriteByte('{')
			i += 2
			continue
		}
		// Escaped close brace: }} → literal }
		if i+1 < len(s) && s[i] == '}' && s[i+1] == '}' {
			result.WriteByte('}')
			i += 2
			continue
		}
		if s[i] != '{' {
			result.WriteByte(s[i])
			i++
			continue
		}
		// Single open brace: look for the matching close.
		close := strings.IndexByte(s[i+1:], '}')
		if close < 0 {
			// No closing brace; write the rest literally.
			result.WriteString(s[i:])
			break
		}
		key := s[i+1 : i+1+close]
		if key == "" {
			return "", fmt.Errorf("empty template variable placeholder \"{}\"")
		}
		val, ok := vars[key]
		if !ok {
			return "", fmt.Errorf("unknown template variable {%s}", key)
		}
		result.WriteString(val)
		i = i + 1 + close + 1
	}
	return result.String(), nil
}

// expandSlice applies ExpandVars to each entry in ss. Errors name the
// pipeline, stage, and field for diagnostics.
func expandSlice(ss []string, vars map[string]string, pipeline, stage, field string) ([]string, error) {
	if len(ss) == 0 {
		return nil, nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		expanded, err := ExpandVars(s, vars)
		if err != nil {
			return nil, fmt.Errorf("instantiate %q stage %q %s[%d]: %w", pipeline, stage, field, i, err)
		}
		out[i] = expanded
	}
	return out, nil
}
