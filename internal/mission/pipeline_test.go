package mission

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePipelineFile(t *testing.T, dir, name, content string) {
	t.Helper()
	pipDir := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipDir, name+".yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const standardPipelineYAML = `name: standard
description: "Design, implement, test, and review"
stages:
  - name: design
    archetype: design
    description: "Produce the design document"
  - name: implement
    archetype: implement
    description: "Write the code"
    inputs_from: design
  - name: test
    archetype: test
    description: "Write and run tests"
    inputs_from: implement
  - name: review
    archetype: review
    description: "Review the diff"
    inputs_from: test
`

const quickPipelineYAML = `name: quick
description: "Implement and review"
stages:
  - name: implement
    archetype: implement
    description: "Write the code"
  - name: review
    archetype: review
    description: "Review the implementation"
    inputs_from: implement
`

const sprintPipelineYAML = `name: sprint
description: "Design, implement, and test a feature"
stages:
  - name: design
    archetype: design
    write_set:
      - "docs/{feature}.md"
    worker: mdm
    success_criteria:
      - "Design doc covers problem"
  - name: implement
    archetype: implement
    write_set:
      - "internal/{feature}/"
    worker: bwk
    inputs_from: design
    budget:
      rounds: 4
      reflection_after_each: true
  - name: test
    archetype: test
    worker: bwk
    inputs_from: implement
    context: "Test the {feature} implementation"
`

func TestPipelineStore_Load(t *testing.T) {
	tests := []struct {
		name       string
		setupRepo  func(t *testing.T, dir string)
		setupGlob  func(t *testing.T, dir string)
		loadName   string
		wantName   string
		wantDesc   string
		wantStages int
		wantErr    bool
		wantNotF   bool
	}{
		{
			name:      "load from global",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "standard", standardPipelineYAML)
			},
			loadName:   "standard",
			wantName:   "standard",
			wantDesc:   "Design, implement, test, and review",
			wantStages: 4,
		},
		{
			name: "load from repo",
			setupRepo: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "quick", quickPipelineYAML)
			},
			setupGlob:  func(t *testing.T, dir string) {},
			loadName:   "quick",
			wantName:   "quick",
			wantDesc:   "Implement and review",
			wantStages: 2,
		},
		{
			name: "repo overrides global",
			setupRepo: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "standard", `name: standard
description: "Repo standard"
stages:
  - name: implement
    archetype: implement
    description: "Just implement"
  - name: review
    archetype: review
    description: "Just review"
    inputs_from: implement
`)
			},
			setupGlob: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "standard", standardPipelineYAML)
			},
			loadName:   "standard",
			wantName:   "standard",
			wantDesc:   "Repo standard",
			wantStages: 2,
		},
		{
			name:      "not found",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {},
			loadName:  "nonexistent",
			wantErr:   true,
			wantNotF:  true,
		},
		{
			name:      "malformed YAML",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "bad", "{{not yaml")
			},
			loadName: "bad",
			wantErr:  true,
		},
		{
			name:      "name defaults to filename",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "minimal", `description: "Minimal pipeline"
stages:
  - name: do
    archetype: task
  - name: check
    archetype: review
    inputs_from: do
`)
			},
			loadName:   "minimal",
			wantName:   "minimal",
			wantDesc:   "Minimal pipeline",
			wantStages: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			global := t.TempDir()
			tc.setupRepo(t, repo)
			tc.setupGlob(t, global)

			s := NewPipelineStore(repo, global)
			p, err := s.Load(tc.loadName)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantNotF && !errors.Is(err, ErrPipelineNotFound) {
					t.Errorf("expected ErrPipelineNotFound, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", p.Name, tc.wantName)
			}
			if p.Description != tc.wantDesc {
				t.Errorf("Description = %q, want %q", p.Description, tc.wantDesc)
			}
			if len(p.Stages) != tc.wantStages {
				t.Errorf("len(Stages) = %d, want %d", len(p.Stages), tc.wantStages)
			}
		})
	}
}

func TestPipelineStore_Load_StageFields(t *testing.T) {
	global := t.TempDir()
	writePipelineFile(t, global, "sprint", sprintPipelineYAML)

	s := NewPipelineStore("", global)
	p, err := s.Load("sprint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(p.Stages) != 3 {
		t.Fatalf("len(Stages) = %d, want 3", len(p.Stages))
	}

	// Stage 0: design
	st := p.Stages[0]
	if st.Name != "design" {
		t.Errorf("stage[0].Name = %q, want design", st.Name)
	}
	if st.Archetype != "design" {
		t.Errorf("stage[0].Archetype = %q, want design", st.Archetype)
	}
	if st.Worker != "mdm" {
		t.Errorf("stage[0].Worker = %q, want mdm", st.Worker)
	}
	if len(st.WriteSet) != 1 || st.WriteSet[0] != "docs/{feature}.md" {
		t.Errorf("stage[0].WriteSet = %v, want [docs/{feature}.md]", st.WriteSet)
	}
	if len(st.SuccessCriteria) != 1 {
		t.Errorf("stage[0].SuccessCriteria = %v, want 1 entry", st.SuccessCriteria)
	}
	if st.InputsFrom != "" {
		t.Errorf("stage[0].InputsFrom = %q, want empty", st.InputsFrom)
	}

	// Stage 1: implement
	st = p.Stages[1]
	if st.InputsFrom != "design" {
		t.Errorf("stage[1].InputsFrom = %q, want design", st.InputsFrom)
	}
	if st.Worker != "bwk" {
		t.Errorf("stage[1].Worker = %q, want bwk", st.Worker)
	}
	if st.Budget == nil {
		t.Fatal("stage[1].Budget is nil, want non-nil")
	}
	if st.Budget.Rounds != 4 {
		t.Errorf("stage[1].Budget.Rounds = %d, want 4", st.Budget.Rounds)
	}
	if !st.Budget.ReflectionAfterEach {
		t.Error("stage[1].Budget.ReflectionAfterEach = false, want true")
	}

	// Stage 2: test
	st = p.Stages[2]
	if st.Context != "Test the {feature} implementation" {
		t.Errorf("stage[2].Context = %q, want template string", st.Context)
	}
}

func TestPipelineStore_List(t *testing.T) {
	tests := []struct {
		name      string
		setupRepo func(t *testing.T, dir string)
		setupGlob func(t *testing.T, dir string)
		want      []string
	}{
		{
			name:      "empty both",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {},
			want:      nil,
		},
		{
			name:      "global only",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "standard", standardPipelineYAML)
				writePipelineFile(t, dir, "quick", quickPipelineYAML)
			},
			want: []string{"quick", "standard"},
		},
		{
			name: "repo only",
			setupRepo: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "standard", standardPipelineYAML)
			},
			setupGlob: func(t *testing.T, dir string) {},
			want:      []string{"standard"},
		},
		{
			name: "merged deduplicated",
			setupRepo: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "standard", standardPipelineYAML)
			},
			setupGlob: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "standard", standardPipelineYAML)
				writePipelineFile(t, dir, "quick", quickPipelineYAML)
			},
			want: []string{"standard", "quick"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			global := t.TempDir()
			tc.setupRepo(t, repo)
			tc.setupGlob(t, global)

			s := NewPipelineStore(repo, global)
			got, err := s.List()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != len(tc.want) {
				t.Fatalf("List() = %v, want %v", got, tc.want)
			}

			wantSet := make(map[string]bool, len(tc.want))
			for _, w := range tc.want {
				wantSet[w] = true
			}
			for _, g := range got {
				if !wantSet[g] {
					t.Errorf("unexpected name %q in List()", g)
				}
			}
		})
	}
}

func TestPipelineStore_Exists(t *testing.T) {
	tests := []struct {
		name      string
		setupRepo func(t *testing.T, dir string)
		setupGlob func(t *testing.T, dir string)
		query     string
		want      bool
	}{
		{
			name:      "exists in global",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "standard", standardPipelineYAML)
			},
			query: "standard",
			want:  true,
		},
		{
			name: "exists in repo",
			setupRepo: func(t *testing.T, dir string) {
				writePipelineFile(t, dir, "quick", quickPipelineYAML)
			},
			setupGlob: func(t *testing.T, dir string) {},
			query:     "quick",
			want:      true,
		},
		{
			name:      "does not exist",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {},
			query:     "nonexistent",
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			global := t.TempDir()
			tc.setupRepo(t, repo)
			tc.setupGlob(t, global)

			s := NewPipelineStore(repo, global)
			got := s.Exists(tc.query)
			if got != tc.want {
				t.Errorf("Exists(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

func TestPipelineStore_EmptyRepoRoot(t *testing.T) {
	global := t.TempDir()
	writePipelineFile(t, global, "standard", standardPipelineYAML)

	s := NewPipelineStore("", global)

	p, err := s.Load("standard")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Name != "standard" {
		t.Errorf("Name = %q, want standard", p.Name)
	}

	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "standard" {
		t.Errorf("List = %v, want [standard]", names)
	}

	if !s.Exists("standard") {
		t.Error("Exists(standard) = false, want true")
	}
}

func TestPipelineStore_NonYAMLIgnored(t *testing.T) {
	global := t.TempDir()
	pipDir := filepath.Join(global, "pipelines")
	if err := os.MkdirAll(pipDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipDir, "README.md"), []byte("# hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(pipDir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	writePipelineFile(t, global, "standard", standardPipelineYAML)

	s := NewPipelineStore("", global)
	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "standard" {
		t.Errorf("List = %v, want [standard]", names)
	}
}

// --- Instantiate tests ---

func TestInstantiate_HappyPath(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)

	p := &Pipeline{
		Name:        "sprint",
		Description: "Design, implement, and test",
		Stages: []Stage{
			{Name: "design", Archetype: "design", WriteSet: []string{"docs/{feature}.md"}, Worker: "mdm",
				SuccessCriteria: []string{"Design doc covers problem"}},
			{Name: "implement", Archetype: "implement", WriteSet: []string{"internal/{feature}/"}, Worker: "bwk",
				InputsFrom: "design", SuccessCriteria: []string{"make check passes"}},
			{Name: "test", Archetype: "test", Worker: "bwk", InputsFrom: "implement",
				Context: "Test the {feature} implementation", WriteSet: []string{"internal/{feature}/"},
				SuccessCriteria: []string{"Coverage does not decrease"}},
		},
	}

	opts := InstantiateOptions{
		PipelineID: "sprint-walk-diff-2026-04-13",
		Vars:       map[string]string{"feature": "walk-diff"},
		Leader:     "claude",
		Evaluator:  "djb",
		Worker:     "fallback-worker",
		Root:       root,
		Now:        now,
	}

	contracts, err := Instantiate(p, opts)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if len(contracts) != 3 {
		t.Fatalf("got %d contracts, want 3", len(contracts))
	}

	// All share the same pipeline ID.
	for i, c := range contracts {
		if c.Pipeline != "sprint-walk-diff-2026-04-13" {
			t.Errorf("contracts[%d].Pipeline = %q, want sprint-walk-diff-2026-04-13", i, c.Pipeline)
		}
	}

	// Stage 0: design
	c := contracts[0]
	if c.Type != "design" {
		t.Errorf("stage 0 Type = %q, want design", c.Type)
	}
	if c.Worker != "mdm" {
		t.Errorf("stage 0 Worker = %q, want mdm (stage override)", c.Worker)
	}
	if len(c.WriteSet) != 1 || c.WriteSet[0] != "docs/walk-diff.md" {
		t.Errorf("stage 0 WriteSet = %v, want [docs/walk-diff.md]", c.WriteSet)
	}
	if len(c.DependsOn) != 0 {
		t.Errorf("stage 0 DependsOn = %v, want empty", c.DependsOn)
	}

	// Stage 1: implement
	c = contracts[1]
	if c.Type != "implement" {
		t.Errorf("stage 1 Type = %q, want implement", c.Type)
	}
	if c.Worker != "bwk" {
		t.Errorf("stage 1 Worker = %q, want bwk", c.Worker)
	}
	if len(c.DependsOn) != 1 || c.DependsOn[0] != contracts[0].MissionID {
		t.Errorf("stage 1 DependsOn = %v, want [%s]", c.DependsOn, contracts[0].MissionID)
	}

	// Stage 2: test
	c = contracts[2]
	if c.Context != "Test the walk-diff implementation" {
		t.Errorf("stage 2 Context = %q, want expanded", c.Context)
	}
	if len(c.DependsOn) != 1 || c.DependsOn[0] != contracts[1].MissionID {
		t.Errorf("stage 2 DependsOn = %v, want [%s]", c.DependsOn, contracts[1].MissionID)
	}

	// Leader and evaluator propagated.
	for i, c := range contracts {
		if c.Leader != "claude" {
			t.Errorf("contracts[%d].Leader = %q, want claude", i, c.Leader)
		}
		if c.Evaluator.Handle != "djb" {
			t.Errorf("contracts[%d].Evaluator = %q, want djb", i, c.Evaluator.Handle)
		}
	}
}

func TestInstantiate_MissingVar(t *testing.T) {
	root := t.TempDir()
	p := &Pipeline{
		Name: "test-pipeline",
		Stages: []Stage{
			{Name: "s1", Archetype: "implement", WriteSet: []string{"internal/{feature}/"},
				SuccessCriteria: []string{"done"}},
		},
	}
	opts := InstantiateOptions{
		Leader: "claude",
		Vars:   map[string]string{},
		Root:   root,
		Now:    time.Now(),
	}
	_, err := Instantiate(p, opts)
	if err == nil {
		t.Fatal("expected error for missing var")
	}
	if !strings.Contains(err.Error(), "{feature}") {
		t.Errorf("error should name the token, got: %v", err)
	}
	if !strings.Contains(err.Error(), "s1") {
		t.Errorf("error should name the stage, got: %v", err)
	}
}

func TestInstantiate_UnknownInputsFrom(t *testing.T) {
	root := t.TempDir()
	p := &Pipeline{
		Name: "bad",
		Stages: []Stage{
			{Name: "s1", Archetype: "implement", InputsFrom: "nonexistent",
				WriteSet: []string{"internal/"}, SuccessCriteria: []string{"done"}},
		},
	}
	opts := InstantiateOptions{Leader: "claude", Root: root, Now: time.Now()}
	_, err := Instantiate(p, opts)
	if err == nil {
		t.Fatal("expected error for unknown inputs_from")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the missing stage, got: %v", err)
	}
}

func TestInstantiate_EmptyLeader(t *testing.T) {
	root := t.TempDir()
	p := &Pipeline{
		Name: "test",
		Stages: []Stage{
			{Name: "s1", Archetype: "implement", WriteSet: []string{"a/"},
				SuccessCriteria: []string{"done"}},
		},
	}
	opts := InstantiateOptions{Leader: "", Root: root, Now: time.Now()}
	_, err := Instantiate(p, opts)
	if err == nil {
		t.Fatal("expected error for empty leader")
	}
	if !strings.Contains(err.Error(), "leader is required") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestInstantiate_StageWorkerOverridesOpts(t *testing.T) {
	root := t.TempDir()
	p := &Pipeline{
		Name: "test",
		Stages: []Stage{
			{Name: "s1", Archetype: "implement", Worker: "stage-worker",
				WriteSet: []string{"a/"}, SuccessCriteria: []string{"done"}},
			{Name: "s2", Archetype: "test", InputsFrom: "s1",
				WriteSet: []string{"a/"}, SuccessCriteria: []string{"done"}},
		},
	}
	opts := InstantiateOptions{
		Leader:    "claude",
		Worker:    "opts-worker",
		Evaluator: "djb",
		Root:      root,
		Now:       time.Now(),
	}
	cs, err := Instantiate(p, opts)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if cs[0].Worker != "stage-worker" {
		t.Errorf("stage 0 Worker = %q, want stage-worker", cs[0].Worker)
	}
	if cs[1].Worker != "opts-worker" {
		t.Errorf("stage 1 Worker = %q, want opts-worker (default)", cs[1].Worker)
	}
}

func TestInstantiate_AutoGeneratedPipelineID(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	p := &Pipeline{
		Name: "sprint",
		Stages: []Stage{
			{Name: "s1", Archetype: "implement", WriteSet: []string{"a/"},
				SuccessCriteria: []string{"done"}},
		},
	}
	opts := InstantiateOptions{
		Leader:    "claude",
		Evaluator: "djb",
		Root:      root,
		Now:       now,
	}
	cs, err := Instantiate(p, opts)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	pid := cs[0].Pipeline
	if !strings.HasPrefix(pid, "sprint-2026-04-13-") {
		t.Errorf("auto-generated ID = %q, want prefix sprint-2026-04-13-", pid)
	}
	// The suffix is 6 hex chars.
	suffix := strings.TrimPrefix(pid, "sprint-2026-04-13-")
	if len(suffix) != 6 {
		t.Errorf("suffix length = %d, want 6 hex chars, got %q", len(suffix), suffix)
	}
}

func TestInstantiate_AutoGeneratedIDCollisionResistance(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	p := &Pipeline{
		Name: "sprint",
		Stages: []Stage{
			{Name: "s1", Archetype: "implement", WriteSet: []string{"a/"},
				SuccessCriteria: []string{"done"}},
		},
	}

	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		opts := InstantiateOptions{
			Leader:    "claude",
			Evaluator: "djb",
			Root:      root,
			Now:       now,
		}
		cs, err := Instantiate(p, opts)
		if err != nil {
			t.Fatalf("Instantiate %d: %v", i, err)
		}
		pid := cs[0].Pipeline
		if seen[pid] {
			t.Fatalf("collision on attempt %d: %s", i, pid)
		}
		seen[pid] = true
	}
}

func TestInstantiate_DryRunNoCounter(t *testing.T) {
	// DryRun must not touch the counter file or create on-disk artifacts.
	root := t.TempDir()
	missionsDir := filepath.Join(root, "missions")
	// Do not create the missions directory; dry-run should not need it.

	p := &Pipeline{
		Name: "sprint",
		Stages: []Stage{
			{Name: "design", Archetype: "design", WriteSet: []string{"docs/x.md"},
				SuccessCriteria: []string{"done"}},
			{Name: "implement", Archetype: "implement", WriteSet: []string{"internal/x/"},
				InputsFrom: "design", SuccessCriteria: []string{"done"}},
		},
	}

	opts := InstantiateOptions{
		Leader:    "claude",
		Evaluator: "djb",
		Root:      "", // no root needed in dry-run
		Now:       time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC),
		DryRun:    true,
	}

	contracts, err := Instantiate(p, opts)
	if err != nil {
		t.Fatalf("Instantiate dry-run: %v", err)
	}
	if len(contracts) != 2 {
		t.Fatalf("got %d contracts, want 2", len(contracts))
	}

	// IDs should be synthetic placeholders.
	if contracts[0].MissionID != "m-dryrun-001" {
		t.Errorf("stage 0 MissionID = %q, want m-dryrun-001", contracts[0].MissionID)
	}
	if contracts[1].MissionID != "m-dryrun-002" {
		t.Errorf("stage 1 MissionID = %q, want m-dryrun-002", contracts[1].MissionID)
	}

	// DependsOn should reference the synthetic ID.
	if len(contracts[1].DependsOn) != 1 || contracts[1].DependsOn[0] != "m-dryrun-001" {
		t.Errorf("stage 1 DependsOn = %v, want [m-dryrun-001]", contracts[1].DependsOn)
	}

	// The missions directory must not have been created.
	if _, err := os.Stat(missionsDir); !os.IsNotExist(err) {
		t.Errorf("missions directory should not exist in dry-run, stat err: %v", err)
	}
}

func TestLoadPipeline_DuplicateStageNames(t *testing.T) {
	global := t.TempDir()
	writePipelineFile(t, global, "dup", `name: dup
description: "Pipeline with duplicate stage names"
stages:
  - name: build
    archetype: implement
    description: "First build"
  - name: build
    archetype: test
    description: "Duplicate build"
`)
	s := NewPipelineStore("", global)
	_, err := s.Load("dup")
	if err == nil {
		t.Fatal("expected error for duplicate stage names")
	}
	if !strings.Contains(err.Error(), "duplicate stage name") {
		t.Errorf("error = %v, want mention of duplicate stage name", err)
	}
	if !strings.Contains(err.Error(), "build") {
		t.Errorf("error = %v, want mention of stage name 'build'", err)
	}
}

func TestLoadPipeline_InvalidPipelineName(t *testing.T) {
	global := t.TempDir()
	writePipelineFile(t, global, "Bad-Name", `name: Bad-Name
description: "Pipeline with uppercase name"
stages:
  - name: s1
    archetype: implement
`)
	s := NewPipelineStore("", global)
	_, err := s.Load("Bad-Name")
	if err == nil {
		t.Fatal("expected error for invalid pipeline name")
	}
	if !strings.Contains(err.Error(), "not a valid slug") {
		t.Errorf("error = %v, want mention of slug validation", err)
	}
}

func TestGeneratePipelineID_InvalidName(t *testing.T) {
	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	_, err := generatePipelineID("Bad Name", now)
	if err == nil {
		t.Fatal("expected error for invalid name")
	}
	if !strings.Contains(err.Error(), "not a valid slug") {
		t.Errorf("error = %v, want mention of slug validation", err)
	}
}

func TestExpandVars(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		vars    map[string]string
		want    string
		wantErr string
	}{
		{name: "no vars", input: "hello world", vars: nil, want: "hello world"},
		{name: "single var", input: "docs/{feature}.md", vars: map[string]string{"feature": "walk-diff"},
			want: "docs/walk-diff.md"},
		{name: "multiple vars", input: "{a}/{b}.txt",
			vars: map[string]string{"a": "src", "b": "main"}, want: "src/main.txt"},
		{name: "missing var", input: "docs/{missing}.md", vars: map[string]string{},
			wantErr: "{missing}"},
		{name: "no braces", input: "plain text", vars: map[string]string{"x": "y"}, want: "plain text"},
		{name: "empty input", input: "", vars: map[string]string{"x": "y"}, want: ""},
		{name: "unclosed brace", input: "docs/{feature", vars: map[string]string{"feature": "x"},
			want: "docs/{feature"},
		{name: "empty key rejected", input: "literal {}", vars: map[string]string{},
			wantErr: "empty template variable"},
		{name: "double brace escape open", input: "{{feature}}", vars: map[string]string{"feature": "foo"},
			want: "{feature}"},
		{name: "double brace mixed", input: "output: {{x: {val}}}", vars: map[string]string{"val": "42"},
			want: "output: {x: 42}"},
		{name: "adjacent tokens", input: "{a}{b}", vars: map[string]string{"a": "x", "b": "y"},
			want: "xy"},
		{name: "double close brace", input: "a}}b", vars: nil, want: "a}b"},
		{name: "only double braces", input: "{{}}", vars: nil, want: "{}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandVars(tt.input, tt.vars)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ExpandVars(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
