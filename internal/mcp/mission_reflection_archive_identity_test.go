package mcp

import (
	"context"
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

func mcpArchiveIdentityTree(t *testing.T, root string) map[string]string {
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

func TestRegisteredMissionReflectionArchiveIdentity(t *testing.T) {
	for _, mirror := range []bool{false, true} {
		t.Run(fmt.Sprintf("published-mirror=%t", mirror), func(t *testing.T) {
			s, p := mutationWarningStore(t)
			id := "m-2026-11-05-181"
			mutationWarningMission(t, s, id)
			for round := 1; round <= 2; round++ {
				if round == 2 {
					n, err := s.AdvanceRound(id, "lead")
					require.NoError(t, err)
					require.Equal(t, 2, n)
				}
				out, err := s.ApplyReflection(id, &mission.Reflection{Mission: id, Round: round, Author: "lead",
					Signals: []string{"checked"}, Recommendation: mission.RecommendationContinue, Reason: fmt.Sprintf("round-%d-prose", round)})
				require.NoError(t, err)
				require.Equal(t, "applied", out.Application)
				require.Equal(t, "confirmed", out.Durability)
				require.Equal(t, "complete", out.Trace)
			}
			states, err := s.ReadReflections(id)
			require.NoError(t, err)
			require.Len(t, states, 2)
			for _, state := range states {
				require.Equal(t, "present", state.DetailState)
				require.Empty(t, state.Diagnostics)
			}
			if mirror {
				data, err := yaml.Marshal(struct {
					Reflections []mission.Reflection `yaml:"reflections"`
				}{[]mission.Reflection{*states[0].Reflection}})
				require.NoError(t, err)
				path := filepath.Join(p.CheckoutRecordRoot(), "missions", id, "reflections.yaml")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, data, 0o600))
			}
			archive := filepath.Join(p.LocalRoot(), "trace", "missions", id, "reflection-1.yaml")
			data, err := os.ReadFile(filepath.Join(filepath.Dir(archive), "reflection-2.yaml"))
			require.NoError(t, err)
			receipts := filepath.Join(p.LocalRoot(), "missions", id, "reflection-intents")
			require.NoError(t, os.WriteFile(filepath.Join(receipts, "2.yaml"), data, 0o600))
			err = os.Remove(filepath.Join(receipts, "1.yaml"))
			require.True(t, err == nil || os.IsNotExist(err), "%v", err)
			require.NoError(t, os.WriteFile(archive, data, 0o600))
			srv, _ := fullServer(t, WithMissionStore(s))
			tool := srv.GetTool("mission")
			require.NotNil(t, tool)
			roots := []string{p.RecordRoot(), p.LocalRoot(), s.Root()}
			if mirror {
				roots = append(roots, p.CheckoutRecordRoot())
			}
			before := make([]map[string]string, len(roots))
			for i, root := range roots {
				before[i] = mcpArchiveIdentityTree(t, root)
			}
			for range 2 {
				result, err := tool.Handler(context.Background(), callTool(map[string]interface{}{"method": "reflections", "mission_id": id}))
				require.NoError(t, err)
				require.False(t, result.IsError, resultText(t, result))
				var got []mission.ReflectionRead
				require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &got))
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
				assert.True(t, got[1].Pending)
				assert.Empty(t, got[1].Diagnostics)
			}
			for i, root := range roots {
				assert.Equal(t, before[i], mcpArchiveIdentityTree(t, root), "registered reads must not recover or backfill")
			}
		})
	}
}
