package mission

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
