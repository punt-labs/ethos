package mission

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// PipelineStore discovers pipeline YAML files from three directories
// (repo-local, active bundle, and global), with repo overriding bundle
// and bundle overriding global.
type PipelineStore struct {
	repo   string // repo pipelines dir, may be empty
	bundle string // active bundle pipelines dir, may be empty
	global string // global pipelines dir
}

// NewPipelineStore creates a two-layer pipeline store (repo + global).
// Equivalent to NewPipelineStoreWithBundle(repoRoot, "", globalRoot).
func NewPipelineStore(repoRoot, globalRoot string) *PipelineStore {
	return NewPipelineStoreWithBundle(repoRoot, "", globalRoot)
}

// NewPipelineStoreWithBundle creates a three-layer pipeline store: repo
// first, then bundle, then global. Any root may be empty, in which case
// that layer is skipped. The bundle layer is read-only by convention.
func NewPipelineStoreWithBundle(repoRoot, bundleRoot, globalRoot string) *PipelineStore {
	var repo, bun, global string
	if repoRoot != "" {
		repo = filepath.Join(repoRoot, "pipelines")
	}
	if bundleRoot != "" {
		bun = filepath.Join(bundleRoot, "pipelines")
	}
	if globalRoot != "" {
		global = filepath.Join(globalRoot, "pipelines")
	}
	return &PipelineStore{repo: repo, bundle: bun, global: global}
}

// Load reads a pipeline by name. Checks repo, then bundle, then global.
func (s *PipelineStore) Load(name string) (*Pipeline, error) {
	for _, dir := range []string{s.repo, s.bundle, s.global} {
		if dir == "" {
			continue
		}
		p, err := loadPipeline(dir, name)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, ErrPipelineNotFound) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("pipeline %q: %w", name, ErrPipelineNotFound)
}

// List returns the names of all discovered pipelines. Higher-precedence
// layers (repo > bundle > global) override lower ones with the same slug.
func (s *PipelineStore) List() ([]string, error) {
	seen := make(map[string]bool)
	var merged []string
	for _, dir := range []string{s.repo, s.bundle, s.global} {
		names, err := listPipelineDir(dir)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				merged = append(merged, n)
			}
		}
	}
	return merged, nil
}

// Exists reports whether a pipeline with the given name exists in any layer.
func (s *PipelineStore) Exists(name string) bool {
	for _, dir := range []string{s.repo, s.bundle, s.global} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name+".yaml")
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
	if !pipelineIDPattern.MatchString(pl.Name) {
		return nil, fmt.Errorf("pipeline %q (file %s): name is not a valid slug: must match ^[a-z0-9][a-z0-9-]*$", pl.Name, name+".yaml")
	}
	if len(pl.Stages) == 0 {
		return nil, fmt.Errorf("pipeline %q: stages list is empty", pl.Name)
	}
	for i, s := range pl.Stages {
		if strings.TrimSpace(s.Name) == "" {
			return nil, fmt.Errorf("pipeline %q: stage[%d] has empty name", pl.Name, i)
		}
		if strings.TrimSpace(s.Archetype) == "" {
			return nil, fmt.Errorf("pipeline %q: stage %q has empty archetype", pl.Name, s.Name)
		}
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
	Archetypes *ArchetypeStore // Optional. When set, applies archetype budget defaults.
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

// generatePipelineID produces a pipeline ID of the form
// <name>-<YYYY-MM-DD>-<6 hex chars>. Returns an error if name is
// not a valid slug.
func generatePipelineID(name string, now time.Time) (string, error) {
	if !pipelineIDPattern.MatchString(name) {
		return "", fmt.Errorf("pipeline name %q is not a valid slug: must match ^[a-z0-9][a-z0-9-]*$", name)
	}
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random suffix: %w", err)
	}
	day := now.UTC().Format("2006-01-02")
	id := fmt.Sprintf("%s-%s-%x", name, day, b)
	if len(id) > maxPipelineIDLen {
		return "", fmt.Errorf("pipeline name %q produces ID of length %d (max %d): shorten the pipeline name", name, len(id), maxPipelineIDLen)
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
