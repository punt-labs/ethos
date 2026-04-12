package mission

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeArchetypeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	arcDir := filepath.Join(dir, "archetypes")
	if err := os.MkdirAll(arcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(arcDir, name+".yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const designYAML = `name: design
description: "Design mission"
budget_default:
  rounds: 2
  reflection_after_each: true
allow_empty_write_set: false
required_fields:
  - context
write_set_constraints:
  - "*.md"
  - "docs/**"
`

const implementYAML = `name: implement
description: "Implementation mission"
budget_default:
  rounds: 3
  reflection_after_each: true
`

const inboxYAML = `name: inbox
description: "Process unread email"
budget_default:
  rounds: 1
  reflection_after_each: false
allow_empty_write_set: true
`

func TestArchetypeStore_Load(t *testing.T) {
	tests := []struct {
		name       string
		setupRepo  func(t *testing.T, dir string)
		setupGlob  func(t *testing.T, dir string)
		loadName   string
		wantName   string
		wantDesc   string
		wantRounds int
		wantErr    bool
		wantNotF   bool
	}{
		{
			name:      "load from global",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "design", designYAML)
			},
			loadName:   "design",
			wantName:   "design",
			wantDesc:   "Design mission",
			wantRounds: 2,
		},
		{
			name: "load from repo",
			setupRepo: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "design", designYAML)
			},
			setupGlob: func(t *testing.T, dir string) {},
			loadName:  "design",
			wantName:  "design",
			wantDesc:  "Design mission",
			wantRounds: 2,
		},
		{
			name: "repo overrides global",
			setupRepo: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "design", `name: design
description: "Repo design"
budget_default:
  rounds: 4
  reflection_after_each: false
`)
			},
			setupGlob: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "design", designYAML)
			},
			loadName:   "design",
			wantName:   "design",
			wantDesc:   "Repo design",
			wantRounds: 4,
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
				writeArchetypeFile(t, dir, "bad", "{{not yaml")
			},
			loadName: "bad",
			wantErr:  true,
		},
		{
			name: "name defaults to filename",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "review", `description: "Review mission"
budget_default:
  rounds: 1
  reflection_after_each: false
`)
			},
			loadName:   "review",
			wantName:   "review",
			wantDesc:   "Review mission",
			wantRounds: 1,
		},
		{
			name: "allow_empty_write_set true",
			setupRepo: func(t *testing.T, dir string) {},
			setupGlob: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "inbox", inboxYAML)
			},
			loadName:   "inbox",
			wantName:   "inbox",
			wantDesc:   "Process unread email",
			wantRounds: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			global := t.TempDir()
			tc.setupRepo(t, repo)
			tc.setupGlob(t, global)

			s := NewArchetypeStore(repo, global)
			a, err := s.Load(tc.loadName)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantNotF && !errors.Is(err, ErrArchetypeNotFound) {
					t.Errorf("expected ErrArchetypeNotFound, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if a.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", a.Name, tc.wantName)
			}
			if a.Description != tc.wantDesc {
				t.Errorf("Description = %q, want %q", a.Description, tc.wantDesc)
			}
			if a.BudgetDefault.Rounds != tc.wantRounds {
				t.Errorf("BudgetDefault.Rounds = %d, want %d", a.BudgetDefault.Rounds, tc.wantRounds)
			}
		})
	}
}

func TestArchetypeStore_Load_Fields(t *testing.T) {
	global := t.TempDir()
	writeArchetypeFile(t, global, "design", designYAML)

	s := NewArchetypeStore("", global)
	a, err := s.Load("design")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !a.BudgetDefault.ReflectionAfterEach {
		t.Error("BudgetDefault.ReflectionAfterEach = false, want true")
	}
	if a.AllowEmptyWriteSet {
		t.Error("AllowEmptyWriteSet = true, want false")
	}
	if len(a.RequiredFields) != 1 || a.RequiredFields[0] != "context" {
		t.Errorf("RequiredFields = %v, want [context]", a.RequiredFields)
	}
	if len(a.WriteSetConstraints) != 2 {
		t.Errorf("WriteSetConstraints = %v, want 2 entries", a.WriteSetConstraints)
	}
}

func TestArchetypeStore_List(t *testing.T) {
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
				writeArchetypeFile(t, dir, "design", designYAML)
				writeArchetypeFile(t, dir, "implement", implementYAML)
			},
			want: []string{"design", "implement"},
		},
		{
			name: "repo only",
			setupRepo: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "design", designYAML)
			},
			setupGlob: func(t *testing.T, dir string) {},
			want:      []string{"design"},
		},
		{
			name: "merged deduplicated",
			setupRepo: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "design", designYAML)
			},
			setupGlob: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "design", designYAML)
				writeArchetypeFile(t, dir, "inbox", inboxYAML)
			},
			want: []string{"design", "inbox"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			global := t.TempDir()
			tc.setupRepo(t, repo)
			tc.setupGlob(t, global)

			s := NewArchetypeStore(repo, global)
			got, err := s.List()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != len(tc.want) {
				t.Fatalf("List() = %v, want %v", got, tc.want)
			}

			// Build set for order-independent comparison.
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

func TestArchetypeStore_Exists(t *testing.T) {
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
				writeArchetypeFile(t, dir, "design", designYAML)
			},
			query: "design",
			want:  true,
		},
		{
			name: "exists in repo",
			setupRepo: func(t *testing.T, dir string) {
				writeArchetypeFile(t, dir, "design", designYAML)
			},
			setupGlob: func(t *testing.T, dir string) {},
			query:     "design",
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

			s := NewArchetypeStore(repo, global)
			got := s.Exists(tc.query)
			if got != tc.want {
				t.Errorf("Exists(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

func TestArchetypeStore_EmptyRepoRoot(t *testing.T) {
	global := t.TempDir()
	writeArchetypeFile(t, global, "design", designYAML)

	s := NewArchetypeStore("", global)

	a, err := s.Load("design")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if a.Name != "design" {
		t.Errorf("Name = %q, want design", a.Name)
	}

	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "design" {
		t.Errorf("List = %v, want [design]", names)
	}

	if !s.Exists("design") {
		t.Error("Exists(design) = false, want true")
	}
}

func TestArchetypeStore_NonYAMLIgnored(t *testing.T) {
	global := t.TempDir()
	arcDir := filepath.Join(global, "archetypes")
	if err := os.MkdirAll(arcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Write a non-YAML file and a subdirectory.
	if err := os.WriteFile(filepath.Join(arcDir, "README.md"), []byte("# hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(arcDir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeArchetypeFile(t, global, "design", designYAML)

	s := NewArchetypeStore("", global)
	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "design" {
		t.Errorf("List = %v, want [design]", names)
	}
}
