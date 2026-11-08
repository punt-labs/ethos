//go:build linux || darwin

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func cliArchiveIdentityTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[path] = info.Mode().String()
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] += string(data)
		}
		return nil
	}))
	return files
}

func TestCLIMissionReflectionArchiveIdentity(t *testing.T) {
	for _, mirror := range []bool{false, true} {
		t.Run(fmt.Sprintf("published-mirror=%t", mirror), func(t *testing.T) {
			se, s, id := fullExportFixture(t)
			n, err := s.AdvanceRound(id, "claude")
			require.NoError(t, err)
			require.Equal(t, 2, n)
			out, err := s.ApplyReflection(id, &mission.Reflection{Mission: id, Round: 2, Author: "claude",
				Signals: []string{"round-two-signal"}, Recommendation: mission.RecommendationContinue, Reason: "round-two-reason"})
			require.NoError(t, err)
			require.Equal(t, "applied", out.Application)
			require.Equal(t, "confirmed", out.Durability)
			require.Equal(t, "complete", out.Trace)
			states, err := s.ReadReflections(id)
			require.NoError(t, err)
			require.Len(t, states, 2)
			for _, state := range states {
				require.Equal(t, "present", state.DetailState)
				require.NotNil(t, state.Decision)
				require.Empty(t, state.Diagnostics)
			}
			events, warnings, err := s.LoadEvents(id)
			require.NoError(t, err)
			require.Empty(t, warnings)
			for _, state := range states {
				count := 0
				for _, event := range events {
					if event.OperationID == state.Decision.ID {
						count++
						require.Equal(t, "accepted", event.Acceptance)
					}
				}
				require.Equal(t, 1, count)
			}
			p, ok := s.RepositoryPolicy()
			require.True(t, ok)
			if mirror {
				data, err := yaml.Marshal(struct {
					Reflections []mission.Reflection `yaml:"reflections"`
				}{[]mission.Reflection{*states[0].Reflection}})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(p.RecordRoot(), "missions", id, "reflections.yaml"), data, 0o600))
			}
			// Establish complete export history before damaging the archive. This
			// supplies sealed original events and the retained delegation prompt.
			_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "healthy-export")
			require.Equal(t, 0, exit, stderr)
			require.FileExists(t, filepath.Join(se.repo, "healthy-export", id, "reflections.yaml"))
			archive := filepath.Join(p.LocalRoot(), "trace", "missions", id, "reflection-1.yaml")
			data, err := os.ReadFile(filepath.Join(filepath.Dir(archive), "reflection-2.yaml"))
			require.NoError(t, err)
			// Retain round 2's genuine receipt bytes, but remove only round 1's.
			receipts := filepath.Join(p.LocalRoot(), "missions", id, "reflection-intents")
			require.NoError(t, os.WriteFile(filepath.Join(receipts, "2.yaml"), data, 0o600))
			err = os.Remove(filepath.Join(receipts, "1.yaml"))
			require.True(t, err == nil || os.IsNotExist(err), "%v", err)
			require.NoError(t, os.WriteFile(archive, data, 0o600))
			before, homeBefore := cliArchiveIdentityTree(t, se.repo), cliArchiveIdentityTree(t, se.home)
			for range 2 {
				stdout, stderr, exit := runCLI(t, se, "mission", "reflections", id, "--json")
				require.Equal(t, 0, exit, stderr)
				var got []mission.ReflectionRead
				require.NoError(t, json.Unmarshal([]byte(stdout), &got))
				require.Len(t, got, 2)
				assert.Equal(t, "present", got[0].DecisionState)
				assert.Equal(t, states[0].Decision, got[0].Decision)
				assert.Equal(t, "unreadable", got[0].DetailState)
				assert.Nil(t, got[0].Reflection)
				assert.False(t, got[0].Pending)
				diagnostic := strings.Join(got[0].Diagnostics, "\n")
				for _, want := range []string{"reflection archive", archive, "requested round 1", "actual round 2"} {
					assert.Contains(t, diagnostic, want)
				}
				assert.Equal(t, "present", got[1].DetailState)
				assert.Equal(t, states[1].Reflection, got[1].Reflection)
				for _, command := range []string{"reflections", "show"} {
					stdout, stderr, exit = runCLI(t, se, "mission", command, id)
					require.Equal(t, 0, exit, stderr)
					assert.Contains(t, stdout, "round 1: decision=present detail=unreadable")
					assert.Contains(t, stdout, "requested round 1, actual round 2")
					assert.Contains(t, stdout, "round 2: decision=present detail=present")
				}
				_, stderr, exit = runCLI(t, se, "mission", "export", id, "--full", "--dir", "damaged-export")
				assert.NotEqual(t, 0, exit, stderr)
				assert.Contains(t, stderr, "full export incomplete")
				assert.Contains(t, stderr, "round 1 decision present; reflection detail unreadable")
				assert.NoDirExists(t, filepath.Join(se.repo, "damaged-export"))
			}
			assert.Equal(t, before, cliArchiveIdentityTree(t, se.repo), "reads/export must retain all evidence and the actual index")
			assert.Equal(t, homeBefore, cliArchiveIdentityTree(t, se.home))
		})
	}
}
