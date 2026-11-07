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

func TestHandleMissionLegacyUnknown(t *testing.T) {
	for _, tc := range []struct {
		round  int
		detail string
	}{{1, "missing"}, {1, "empty"}, {3, "missing"}, {3, "empty"}, {3, "prefix"}, {3, "later"}} {
		t.Run(fmt.Sprintf("round=%d/%s", tc.round, tc.detail), func(t *testing.T) {
			root := t.TempDir()
			id := "m-2026-11-05-162"
			c := uiTestContract(id)
			c.Budget.Rounds, c.CurrentRound = 3, tc.round
			c.RoundDecisions = nil
			// Bypass repository initialization: this is a real legacy omission.
			body, err := yaml.Marshal(c)
			require.NoError(t, err)
			require.NotContains(t, string(body), "round_decisions")
			path := mission.RepoStatePath(root, "missions", id, "contract.yaml")
			writeUIFixture(t, path, body)
			before := body
			if tc.detail != "missing" {
				var reflections []mission.Reflection
				if tc.detail == "prefix" || tc.detail == "later" {
					round := 1
					if tc.detail == "later" {
						round = tc.round
					}
					reflections = []mission.Reflection{{Mission: id, Round: round, Author: "claude",
						Signals: []string{"retained legacy signal"}, Recommendation: mission.RecommendationContinue, Reason: "retained legacy reason"}}
				}
				body, err = yaml.Marshal(struct {
					Reflections []mission.Reflection `yaml:"reflections"`
				}{reflections})
				require.NoError(t, err)
				if tc.detail == "empty" {
					body = nil
				}
				writeUIFixture(t, filepath.Join(filepath.Dir(path), "reflections.yaml"), body)
			}
			srv, err := NewServer(root, root)
			require.NoError(t, err)
			srv.globalRoot, srv.globalSessionsDir = t.TempDir(), t.TempDir()
			store, err := srv.missionStore()
			require.NoError(t, err)
			loaded, err := store.Load(id)
			require.NoError(t, err)
			require.Nil(t, loaded.RoundDecisions)
			for range 2 {
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missions/"+id, nil))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				for round := 1; round <= tc.round; round++ {
					detail := "absent"
					if (tc.detail == "prefix" && round == 1) || (tc.detail == "later" && round == tc.round) {
						detail = "present"
					}
					assert.Contains(t, rec.Body.String(), fmt.Sprintf("Round %d: decision=legacy_unknown detail=%s", round, detail))
				}
				assert.NotContains(t, rec.Body.String(), "decision=absent")
				if tc.detail == "prefix" || tc.detail == "later" {
					assert.Contains(t, rec.Body.String(), "retained legacy reason")
					assert.Contains(t, rec.Body.String(), "retained legacy signal")
				}
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.NoDirExists(t, filepath.Join(root, ".punt-labs", "local"), "UI reads must not initialize recovery state")
		})
	}
}
