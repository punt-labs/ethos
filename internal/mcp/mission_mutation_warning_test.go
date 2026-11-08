package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func mutationWarningStore(t *testing.T) (*mission.Store, layout.Policy) {
	t.Helper()
	p, err := layout.NewRepository(t.TempDir(), t.TempDir(), layout.RecordBlame)
	require.NoError(t, err)
	return mission.NewRepositoryStore(p, t.TempDir()).WithSessionID("mutation-warning"), p
}

func mutationWarningMission(t *testing.T, s *mission.Store, id string) {
	t.Helper()
	stamp := "2026-11-04T10:00:00Z"
	require.NoError(t, s.Create(&mission.Contract{MissionID: id, Status: mission.StatusOpen,
		CreatedAt: stamp, UpdatedAt: stamp, Leader: "lead", Worker: "bwk",
		Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: stamp}, WriteSet: []string{id + ".go"},
		SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 3}, CurrentRound: 1}))
}

func mutationWarningResult(id string) *mission.Result {
	return &mission.Result{Mission: id, Round: 1, Author: "bwk", Verdict: mission.VerdictPass,
		Confidence: 0.9, Evidence: []mission.EvidenceCheck{{Name: "focused check", Status: "pass"}}}
}

// Recreate the durable receipt left by an interrupted optional archive, without
// a Store hook or mutable diagnostic state shared by requests.
func mutationWarningReceipt(t *testing.T, s *mission.Store, p layout.Policy, id string) string {
	t.Helper()
	out, err := s.ApplyReflection(id, &mission.Reflection{Mission: id, Round: 1, Author: "lead",
		Converging: true, Signals: []string{"checked"}, Recommendation: mission.RecommendationContinue,
		Reason: "accepted reflection"})
	require.NoError(t, err)
	require.Equal(t, "complete", out.Trace)
	archive := filepath.Join(p.LocalRoot(), "trace", "missions", id, "reflection-1.yaml")
	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	receipt := filepath.Join(p.LocalRoot(), "missions", id, "reflection-intents", "1.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(receipt), 0o700))
	require.NoError(t, os.WriteFile(receipt, data, 0o600))
	var detail map[string]any
	require.NoError(t, yaml.Unmarshal(data, &detail))
	detail["reflection"].(map[string]any)["reason"] = "divergent optional prose"
	data, err = yaml.Marshal(detail)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(archive, data, 0o600))
	return receipt
}

func TestRegisteredMissionMutationWarnings(t *testing.T) {
	for _, method := range []string{"result", "advance", "close", "abandon"} {
		t.Run(method, func(t *testing.T) {
			s, p := mutationWarningStore(t)
			id := "m-2026-11-04-821"
			mutationWarningMission(t, s, id)
			if method == "close" {
				require.NoError(t, s.AppendResult(id, mutationWarningResult(id)))
			}
			receipt := mutationWarningReceipt(t, s, p, id)
			before, err := os.ReadFile(receipt)
			require.NoError(t, err)
			srv, _ := fullServer(t, WithMissionStore(s))
			body, err := yaml.Marshal(mutationWarningResult(id))
			require.NoError(t, err)
			r, err := srv.GetTool("mission").Handler(context.Background(), callTool(map[string]interface{}{
				"method": method, "mission_id": id, "reason": "cancelled", "result": string(body),
			}))
			require.NoError(t, err)
			require.False(t, r.IsError, resultText(t, r))
			var payload struct {
				Mission  string   `json:"mission_id"`
				Warnings []string `json:"warnings"`
			}
			require.NoError(t, json.Unmarshal([]byte(resultText(t, r)), &payload))
			assert.Equal(t, id, payload.Mission)
			require.Len(t, payload.Warnings, 1)
			assert.Contains(t, payload.Warnings[0], "accepted; optional reflection trace pending")
			assert.Contains(t, payload.Warnings[0], "optional reflection detail conflict")
			after, err := os.ReadFile(receipt)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			c, err := s.Load(id)
			require.NoError(t, err)
			switch method {
			case "result":
				result, err := s.LoadResult(id, 1)
				require.NoError(t, err)
				require.NotNil(t, result)
			case "advance":
				assert.Equal(t, 2, c.CurrentRound)
			case "close":
				assert.Equal(t, mission.StatusClosed, c.Status)
			case "abandon":
				assert.Equal(t, mission.StatusAbandoned, c.Status)
			}
		})
	}
}

func TestRegisteredMissionMutationWarningsConcurrent(t *testing.T) {
	s, p := mutationWarningStore(t)
	srv, _ := fullServer(t, WithMissionStore(s))
	tool := srv.GetTool("mission")
	const count = 16
	ids := make([]string, count)
	requests := make([]mcplib.CallToolRequest, count)
	for i := range ids {
		id := fmt.Sprintf("m-2026-11-04-%03d", 830+i)
		ids[i] = id
		mutationWarningMission(t, s, id)
		if i%2 == 0 {
			mutationWarningReceipt(t, s, p, id)
		}
		body, err := yaml.Marshal(mutationWarningResult(id))
		require.NoError(t, err)
		requests[i] = callTool(map[string]interface{}{"method": "result", "mission_id": id, "result": string(body)})
	}
	type answer struct {
		index  int
		result *mcplib.CallToolResult
		err    error
	}
	answers := make(chan answer, count)
	start := make(chan struct{})
	for i, req := range requests {
		go func() {
			<-start
			r, err := tool.Handler(context.Background(), req)
			answers <- answer{i, r, err}
		}()
	}
	close(start)
	completed := make([]answer, 0, count)
	for range count {
		completed = append(completed, <-answers)
	}
	for _, a := range completed {
		require.NoError(t, a.err)
		require.False(t, a.result.IsError, resultText(t, a.result))
		var payload struct {
			Mission  string   `json:"mission_id"`
			Warnings []string `json:"warnings"`
		}
		require.NoError(t, json.Unmarshal([]byte(resultText(t, a.result)), &payload))
		assert.Equal(t, ids[a.index], payload.Mission)
		if a.index%2 == 0 {
			require.Len(t, payload.Warnings, 1)
			assert.Contains(t, payload.Warnings[0], ids[a.index])
			assert.Contains(t, payload.Warnings[0], "optional reflection detail conflict")
		} else {
			assert.Empty(t, payload.Warnings, strings.Join(payload.Warnings, "\n"))
		}
	}
}

func TestRegisteredMissionMutationAuthorityFailure(t *testing.T) {
	for _, fault := range []string{"receipt", "pending", "authority", "event"} {
		t.Run(fault, func(t *testing.T) {
			s, p := mutationWarningStore(t)
			id := "m-2026-11-04-822"
			mutationWarningMission(t, s, id)
			receipt := mutationWarningReceipt(t, s, p, id)
			want := "reflection receipt conflicts with shared decision"
			switch fault {
			case "receipt":
				data, err := os.ReadFile(receipt)
				require.NoError(t, err)
				var in map[string]any
				require.NoError(t, yaml.Unmarshal(data, &in))
				oldID := in["decision"].(map[string]any)["id"].(string)
				data = []byte(strings.ReplaceAll(string(data), oldID, strings.Repeat("a", 32)))
				require.NoError(t, os.WriteFile(receipt, data, 0o600))
			case "pending":
				c, err := s.Load(id)
				require.NoError(t, err)
				c.RoundDecisions = nil
				data, err := yaml.Marshal(c)
				require.NoError(t, err)
				path, err := s.ContractPath(id)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data, 0o600))
				want = "pending reflection preparation"
			case "authority":
				path, err := s.ContractPath(id)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, []byte("broken: [yaml"), 0o600))
				want = "mission authority unreadable"
			case "event":
				path := filepath.Join(p.LocalRoot(), "missions", id, "mutation-warning.log.jsonl")
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				data = []byte(strings.ReplaceAll(string(data), `"actor":"lead"`, `"actor":"other"`))
				require.NoError(t, os.WriteFile(path, data, 0o600))
				want = "required event conflicts with receipt"
			}
			srv, _ := fullServer(t, WithMissionStore(s))
			body, err := yaml.Marshal(mutationWarningResult(id))
			require.NoError(t, err)
			r, err := srv.GetTool("mission").Handler(context.Background(), callTool(map[string]interface{}{
				"method": "result", "mission_id": id, "result": string(body),
			}))
			require.NoError(t, err)
			require.True(t, r.IsError, resultText(t, r))
			assert.Contains(t, resultText(t, r), want)
			result, err := s.LoadResult(id, 1)
			require.NoError(t, err)
			assert.Nil(t, result)
		})
	}
}

func TestRegisteredMissionMutationWarningsBindingFailure(t *testing.T) {
	for _, method := range []string{"close", "abandon"} {
		t.Run(method, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ETHOS_SESSION", "warning-binding")
			s, p := mutationWarningStore(t)
			id := "m-2026-11-04-823"
			mutationWarningMission(t, s, id)
			if method == "close" {
				require.NoError(t, s.AppendResult(id, mutationWarningResult(id)))
			}
			mutationWarningReceipt(t, s, p, id)
			global := filepath.Join(home, ".punt-labs", "ethos")
			ss := session.NewStore(global)
			seedSessionRoster(t, ss, "warning-binding")
			binding := mission.ActiveMissionPath(global, "warning-binding")
			require.NoError(t, os.MkdirAll(binding, 0o700))
			srv, _ := fullServer(t, WithMissionStore(s), WithSessionStore(ss))
			r, err := srv.GetTool("mission").Handler(context.Background(), callTool(map[string]interface{}{
				"method": method, "mission_id": id, "reason": "cancelled",
			}))
			require.NoError(t, err)
			require.True(t, r.IsError, resultText(t, r))
			var payload struct {
				Mission  string   `json:"mission_id"`
				Status   string   `json:"status"`
				Error    string   `json:"error"`
				Warnings []string `json:"warnings"`
			}
			require.NoError(t, json.Unmarshal([]byte(resultText(t, r)), &payload))
			assert.Equal(t, id, payload.Mission)
			wantStatus := mission.StatusClosed
			if method == "abandon" {
				wantStatus = mission.StatusAbandoned
			}
			assert.Equal(t, wantStatus, payload.Status)
			assert.Contains(t, payload.Error, "is "+wantStatus)
			assert.Contains(t, payload.Error, "not cleared and it may still stamp commit trailers")
			assert.Contains(t, payload.Error, "reading active mission")
			require.Len(t, payload.Warnings, 1)
			assert.Contains(t, payload.Warnings[0], "accepted; optional reflection trace pending")
			c, err := s.Load(id)
			require.NoError(t, err)
			assert.Equal(t, wantStatus, c.Status, "terminal acceptance stands")
			assert.DirExists(t, binding, "failed cleanup did not erase evidence")
		})
	}
}

func TestRegisteredMissionMutationWarningsRefused(t *testing.T) {
	for _, method := range []string{"close", "abandon", "advance", "result"} {
		for _, fault := range []string{"guard", "persistence"} {
			t.Run(method+"/"+fault, func(t *testing.T) {
				s, p := mutationWarningStore(t)
				id := "m-2026-11-04-824"
				mutationWarningMission(t, s, id)
				if method == "abandon" && fault == "guard" || method == "close" && fault == "persistence" {
					require.NoError(t, s.AppendResult(id, mutationWarningResult(id)))
				}
				if method == "advance" && fault == "guard" {
					require.NoError(t, s.AppendResult(id, mutationWarningResult(id)))
				}
				mutationWarningReceipt(t, s, p, id)
				if method == "advance" && fault == "guard" {
					_, _, err := s.CloseWithOutcome(id, mission.StatusClosed)
					require.NoError(t, err)
				}
				if fault == "persistence" {
					require.NoError(t, os.Remove(p.TempDir()))
					require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked staging"), 0o600))
				}
				path, err := s.ContractPath(id)
				require.NoError(t, err)
				before, err := os.ReadFile(path)
				require.NoError(t, err)
				result := mutationWarningResult(id)
				if fault == "guard" {
					result.Round = 2
				}
				body, err := yaml.Marshal(result)
				require.NoError(t, err)
				srv, _ := fullServer(t, WithMissionStore(s))
				r, err := srv.GetTool("mission").Handler(context.Background(), callTool(map[string]interface{}{
					"method": method, "mission_id": id, "reason": "cancelled", "result": string(body),
				}))
				require.NoError(t, err)
				require.True(t, r.IsError, resultText(t, r))
				var payload struct {
					Error    string   `json:"error"`
					Warnings []string `json:"warnings"`
					Status   string   `json:"status"`
				}
				require.NoError(t, json.Unmarshal([]byte(resultText(t, r)), &payload))
				assert.Contains(t, payload.Error, "failed to ")
				if fault == "guard" {
					want := map[string]string{"close": "no result artifact", "abandon": "result artifact(s) exist", "advance": "terminal state", "result": "result round 2 does not match"}
					assert.Contains(t, payload.Error, want[method])
				} else {
					assert.Contains(t, payload.Error, "not a directory")
				}
				assert.Empty(t, payload.Status, "a refused mutation must not claim a terminal transition")
				require.Len(t, payload.Warnings, 1)
				assert.Contains(t, payload.Warnings[0], "accepted; optional reflection trace pending")
				assert.NotContains(t, payload.Warnings[0], "installed;")
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, before, after)
			})
		}
	}
}

func TestRegisteredMissionMutationDelegationWarning(t *testing.T) {
	s, p := mutationWarningStore(t)
	id := "m-2026-11-04-825"
	mutationWarningMission(t, s, id)
	require.NoError(t, s.AppendResult(id, mutationWarningResult(id)))
	path, err := mission.WriteDelegationSkeletonWithPolicy(p, id, "d-warning", mission.DelegationSkeleton{Tier: mission.TierB})
	require.NoError(t, err)
	// A real reachable ancillary error needs no exported fault hook: malformed
	// delegation evidence remains on disk while the accepted close stands.
	data := []byte("broken: [yaml")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	srv, _ := fullServer(t, WithMissionStore(s))
	r, err := srv.GetTool("mission").Handler(context.Background(), callTool(map[string]interface{}{
		"method": "close", "mission_id": id,
	}))
	require.NoError(t, err)
	require.False(t, r.IsError, resultText(t, r))
	var payload struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, r)), &payload))
	assert.Equal(t, mission.StatusClosed, payload.Status)
	require.Len(t, payload.Warnings, 1)
	assert.Contains(t, payload.Warnings[0], "loading delegation d-warning for close")
	assert.Contains(t, payload.Warnings[0], "yaml")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, data, after, "unreadable ancillary evidence was not overwritten")
	c, err := s.Load(id)
	require.NoError(t, err)
	assert.Equal(t, mission.StatusClosed, c.Status)
}

func TestRegisteredMissionMutationWarningsRefusedConcurrent(t *testing.T) {
	s, p := mutationWarningStore(t)
	srv, _ := fullServer(t, WithMissionStore(s))
	tool := srv.GetTool("mission")
	const count = 12
	ids := make([]string, count)
	requests := make([]mcplib.CallToolRequest, count)
	for i := range ids {
		id := fmt.Sprintf("m-2026-11-04-%03d", 870+i)
		ids[i] = id
		mutationWarningMission(t, s, id)
		if i%2 == 0 {
			mutationWarningReceipt(t, s, p, id)
		}
		result := mutationWarningResult(id)
		result.Round = 2
		body, err := yaml.Marshal(result)
		require.NoError(t, err)
		requests[i] = callTool(map[string]interface{}{"method": "result", "mission_id": id, "result": string(body)})
	}
	type answer struct {
		index  int
		result *mcplib.CallToolResult
		err    error
	}
	answers := make(chan answer, count)
	start := make(chan struct{})
	for i, req := range requests {
		go func() {
			<-start
			r, err := tool.Handler(context.Background(), req)
			answers <- answer{i, r, err}
		}()
	}
	close(start)
	completed := make([]answer, 0, count)
	for range count {
		completed = append(completed, <-answers)
	}
	for _, a := range completed {
		require.NoError(t, a.err)
		require.True(t, a.result.IsError, resultText(t, a.result))
		text := resultText(t, a.result)
		if a.index%2 == 0 {
			var payload struct {
				Error    string   `json:"error"`
				Warnings []string `json:"warnings"`
			}
			require.NoError(t, json.Unmarshal([]byte(text), &payload))
			assert.Contains(t, payload.Error, ids[a.index])
			require.Len(t, payload.Warnings, 1)
			assert.Contains(t, payload.Warnings[0], ids[a.index])
			assert.Contains(t, payload.Warnings[0], "optional reflection detail conflict")
		} else {
			assert.Contains(t, text, "failed to record result")
			assert.Contains(t, text, ids[a.index])
			assert.NotContains(t, text, "warnings", "warning-free errors retain the plain-text contract")
			assert.NotContains(t, text, "optional reflection")
		}
		r, err := s.LoadResult(ids[a.index], 1)
		require.NoError(t, err)
		assert.Nil(t, r)
	}
}
