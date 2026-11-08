package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// Write the old contract directly: repository Create initializes v1 authority
// and would test an initialized empty history rather than a legacy omission.
func legacyReflectionReader(t *testing.T, repository bool, round int) (*Store, *Contract, string) {
	t.Helper()
	root := t.TempDir()
	s := NewStore(root)
	path := filepath.Join(root, "missions", "m-2026-11-05-162.yaml")
	if repository {
		p, err := layout.NewRepository(root, root, layout.RecordBlame)
		require.NoError(t, err)
		s = NewRepositoryStore(p, t.TempDir())
		path = RepoStatePath(root, "missions", "m-2026-11-05-162", "contract.yaml")
	}
	c := newContract("m-2026-11-05-162")
	c.Budget.Rounds, c.CurrentRound = 3, round
	c.RoundDecisions = nil
	data, err := yaml.Marshal(c)
	require.NoError(t, err)
	require.NotContains(t, string(data), "round_decisions")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	require.Nil(t, loaded.RoundDecisions)
	return s, c, root
}

// Include directories and links as well as bytes so a read cannot quietly
// initialize a local tree, a lock, or authority while returning correct output.
func legacyReflectionTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += target
		} else if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += string(data)
		}
		out[rel] = value
		return nil
	}))
	return out
}

func TestLegacyReflectionReadAvailability(t *testing.T) {
	for _, repository := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			round  int
			body   string
			detail []int
		}{
			{name: "missing", round: 1},
			{name: "empty", round: 1, body: " \n\t\n"},
			{name: "empty-list", round: 3, body: "reflections: []\n"},
			{name: "prefix-only", round: 3, detail: []int{1}},
			{name: "later-only", round: 3, detail: []int{3}},
			{name: "gap", round: 3, detail: []int{1, 3}},
			{name: "complete", round: 3, detail: []int{1, 2, 3}},
		} {
			t.Run(fmt.Sprintf("repository=%t/%s", repository, tc.name), func(t *testing.T) {
				s, c, root := legacyReflectionReader(t, repository, tc.round)
				path := mustReflectionsPath(t, s, c.MissionID)
				var reflections []Reflection
				for _, round := range tc.detail {
					reflections = append(reflections, *reflectionFor(c.MissionID, round, RecommendationContinue))
				}
				body := []byte(tc.body)
				if len(reflections) > 0 {
					var err error
					body, err = yaml.Marshal(reflectionsFile{Reflections: reflections})
					require.NoError(t, err)
				}
				if tc.name != "missing" {
					require.NoError(t, os.WriteFile(path, body, 0o600))
				}
				before := legacyReflectionTree(t, root)
				for range 2 {
					states, err := s.ReadReflections(c.MissionID)
					require.NoError(t, err)
					require.Len(t, states, tc.round)
					for i, state := range states {
						assert.Equal(t, i+1, state.Round)
						assert.Equal(t, "legacy_unknown", state.DecisionState)
						assert.Nil(t, state.Decision)
						assert.False(t, state.Pending)
						assert.Empty(t, state.Diagnostics)
						var want *Reflection
						for _, r := range reflections {
							if r.Round == state.Round {
								want = &r
							}
						}
						if want == nil {
							assert.Equal(t, "absent", state.DetailState)
							assert.Nil(t, state.Reflection, "no fabricated empty Reflection")
						} else {
							assert.Equal(t, "present", state.DetailState)
							assert.Equal(t, want, state.Reflection)
						}
					}
				}
				assert.Equal(t, before, legacyReflectionTree(t, root))
				loaded, err := s.Load(c.MissionID)
				require.NoError(t, err)
				assert.Nil(t, loaded.RoundDecisions)
			})
		}
	}
}

func TestLegacyReflectionReadRejectsUnreadableDetail(t *testing.T) {
	for _, fault := range []string{"truncated", "wrong-type", "directory", "symlink", "dangling-symlink", "future-round"} {
		t.Run(fault, func(t *testing.T) {
			s, c, root := legacyReflectionReader(t, true, 1)
			path := mustReflectionsPath(t, s, c.MissionID)
			switch fault {
			case "truncated":
				require.NoError(t, os.WriteFile(path, []byte("reflections: [\n"), 0o600))
			case "wrong-type":
				require.NoError(t, os.WriteFile(path, []byte("reflections: not-a-list\n"), 0o600))
			case "directory":
				require.NoError(t, os.Mkdir(path, 0o700))
			case "symlink", "dangling-symlink":
				target := filepath.Join(root, "link-target")
				if fault == "symlink" {
					require.NoError(t, os.WriteFile(target, []byte("reflections: []\n"), 0o600))
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "future-round":
				data, err := yaml.Marshal(reflectionsFile{Reflections: []Reflection{*reflectionFor(c.MissionID, 2, RecommendationContinue)}})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data, 0o600))
			}
			before := legacyReflectionTree(t, root)
			states, err := s.ReadReflections(c.MissionID)
			require.Error(t, err, "unreadable detail must not become empty-success")
			assert.Nil(t, states)
			assert.Equal(t, before, legacyReflectionTree(t, root))
		})
	}
}

func TestLegacyReflectionReadConcurrent(t *testing.T) {
	s, c, root := legacyReflectionReader(t, true, 3)
	before := legacyReflectionTree(t, root)
	type result struct {
		states []ReflectionRead
		err    error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for range cap(results) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states, err := s.ReadReflections(c.MissionID)
			results <- result{states, err}
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		require.NoError(t, r.err)
		require.Len(t, r.states, 3)
		for _, state := range r.states {
			assert.Equal(t, "legacy_unknown", state.DecisionState)
			assert.Nil(t, state.Reflection)
		}
	}
	assert.Equal(t, before, legacyReflectionTree(t, root))
}

func TestLegacyReflectionFullExportIncomplete(t *testing.T) {
	for _, detail := range []string{"missing", "empty", "partial"} {
		t.Run(detail, func(t *testing.T) {
			s, c, root := legacyReflectionReader(t, true, 3)
			path := mustReflectionsPath(t, s, c.MissionID)
			if detail == "empty" {
				require.NoError(t, os.WriteFile(path, nil, 0o600))
			} else if detail == "partial" {
				data, err := yaml.Marshal(reflectionsFile{Reflections: []Reflection{*reflectionFor(c.MissionID, 1, RecommendationContinue)}})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data, 0o600))
			}
			before, err := os.ReadFile(mustContractPath(t, s, c.MissionID))
			require.NoError(t, err)
			files, err := s.SnapshotFullTrace(c.MissionID)
			require.ErrorContains(t, err, "full export incomplete")
			assert.ErrorContains(t, err, "legacy_unknown")
			assert.Nil(t, files)
			after, err := os.ReadFile(mustContractPath(t, s, c.MissionID))
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.NoFileExists(t, RepoStatePath(root, "missions", c.MissionID, "corrections.yaml"))
		})
	}
}

func TestLegacyReflectionLoadCompleteDetail(t *testing.T) {
	s, c, root := legacyReflectionReader(t, true, 3)
	var reflections []Reflection
	for round := 1; round <= c.CurrentRound; round++ {
		reflections = append(reflections, *reflectionFor(c.MissionID, round, RecommendationContinue))
	}
	data, err := yaml.Marshal(reflectionsFile{Reflections: reflections})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mustReflectionsPath(t, s, c.MissionID), data, 0o600))
	before := legacyReflectionTree(t, root)
	loaded, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err, "known complete legacy prose remains usable without manufacturing shared decisions")
	assert.Equal(t, reflections, loaded)
	assert.Equal(t, before, legacyReflectionTree(t, root))
	contract, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Nil(t, contract.RoundDecisions)
}
