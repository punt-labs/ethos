//go:build !windows

package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func uiArchiveIdentityTree(t *testing.T, root string) map[string]string {
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

func TestHandleMissionReflectionArchiveIdentity(t *testing.T) {
	for _, mirror := range []bool{false, true} {
		t.Run(fmt.Sprintf("published-mirror=%t", mirror), func(t *testing.T) {
			root, global := t.TempDir(), t.TempDir()
			s := mission.NewStoreWithRoots(root, global).WithSessionID("archive-identity")
			id := "m-2026-11-05-181"
			c := uiTestContract(id)
			c.Budget.Rounds = 2
			require.NoError(t, s.Create(c))
			for round := 1; round <= 2; round++ {
				if round == 2 {
					n, err := s.AdvanceRound(id, c.Leader)
					require.NoError(t, err)
					require.Equal(t, 2, n)
				}
				out, err := s.ApplyReflection(id, &mission.Reflection{Mission: id, Round: round, Author: c.Leader,
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
			p, ok := s.RepositoryPolicy()
			require.True(t, ok)
			if mirror {
				data, err := yaml.Marshal(struct {
					Reflections []mission.Reflection `yaml:"reflections"`
				}{[]mission.Reflection{*states[0].Reflection}})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(p.RecordRoot(), "missions", id, "reflections.yaml"), data, 0o600))
			}
			archive := filepath.Join(p.LocalRoot(), "trace", "missions", id, "reflection-1.yaml")
			data, err := os.ReadFile(filepath.Join(filepath.Dir(archive), "reflection-2.yaml"))
			require.NoError(t, err)
			receipts := filepath.Join(p.LocalRoot(), "missions", id, "reflection-intents")
			require.NoError(t, os.WriteFile(filepath.Join(receipts, "2.yaml"), data, 0o600))
			err = os.Remove(filepath.Join(receipts, "1.yaml"))
			require.True(t, err == nil || os.IsNotExist(err), "%v", err)
			require.NoError(t, os.WriteFile(archive, data, 0o600))
			srv, err := NewServer(root, root)
			require.NoError(t, err)
			srv.globalRoot, srv.globalSessionsDir = global, t.TempDir()
			before, globalBefore := uiArchiveIdentityTree(t, root), uiArchiveIdentityTree(t, global)
			for range 2 {
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missions/"+id, nil))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "Round 1: decision=present detail=unreadable")
				assert.Contains(t, rec.Body.String(), "reflection archive")
				assert.Contains(t, rec.Body.String(), "reflection-1.yaml")
				assert.Contains(t, rec.Body.String(), "requested round 1, actual round 2")
				assert.Contains(t, rec.Body.String(), "continue by "+c.Leader)
				assert.NotContains(t, rec.Body.String(), "round-1-prose")
				assert.Contains(t, rec.Body.String(), "Round 2: decision=present detail=present")
				assert.Contains(t, rec.Body.String(), "round-2-prose")
			}
			assert.Equal(t, before, uiArchiveIdentityTree(t, root))
			assert.Equal(t, globalBefore, uiArchiveIdentityTree(t, global))
		})
	}
}
