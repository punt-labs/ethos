package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/punt-labs/ethos/v4/internal/vendor"
)

type missionWarningDisplay struct {
	Output struct {
		Event   string `json:"hookEventName"`
		Summary string `json:"updatedMCPToolOutput"`
		Context string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Feed the actual registered tool's content through the host's PostToolUse
// envelope, not a reconstructed success object or a private formatter call.
func mutationFormatPayload(tool, method string, r *mcplib.CallToolResult) ([]byte, error) {
	blocks := make([]map[string]any, len(r.Content))
	for i, block := range r.Content {
		text, ok := block.(mcplib.TextContent)
		if !ok {
			return nil, fmt.Errorf("expected text content, got %T", block)
		}
		blocks[i] = map[string]any{"type": text.Type, "text": text.Text, "is_error": r.IsError}
	}
	return json.Marshal(map[string]any{
		"tool_name": "mcp__ethos__" + tool, "tool_input": map[string]string{"method": method}, "tool_response": blocks,
	})
}

func formatRegisteredMutation(t *testing.T, tool, method string, r *mcplib.CallToolResult) missionWarningDisplay {
	t.Helper()
	data, err := mutationFormatPayload(tool, method, r)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, hook.HandleFormatOutput(bytes.NewReader(data), &out))
	var display missionWarningDisplay
	if r.IsError {
		assert.Empty(t, out.String(), "registered MCP errors must remain unchanged for the host")
		return display
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &display))
	assert.Equal(t, "PostToolUse", display.Output.Event)
	return display
}

func TestRegisteredMissionFormatWarnings(t *testing.T) {
	// The mission schema's seven mutations all emit []string warnings. Read
	// methods show/results/log also emit them; reflections uses diagnostics.
	methods := []string{"create", "result", "advance", "close", "abandon", "reflect", "correct"}
	require.ElementsMatch(t, methods, storeWrites["mission"], "new mutation methods need an accepted-warning fixture")
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			s, p := mutationWarningStore(t)
			srv, identities := fullServer(t, WithMissionStore(s))
			recoveryWarningAuthor(t, identities)
			tool := srv.GetTool("mission")
			require.NotNil(t, tool)
			assert.Contains(t, methodEnum(t, tool.Tool), method)
			id := "m-2026-11-05-197"
			args := map[string]interface{}{"method": method, "mission_id": id, "reason": "cancelled"}
			var receipt string
			var before []byte
			switch method {
			case "create":
				// The evaluator must genuinely resolve before creation accepts.
				args["contract"] = strings.ReplaceAll(strings.ReplaceAll(validContractYAML, "djb", "author"), "bead:", "ticket:")
			case "reflect", "correct":
				req := recoveryWarningRequest(t, s, p, id, method, true, false)
				args = req.GetArguments()
			default:
				mutationWarningMission(t, s, id)
				if method == "close" {
					require.NoError(t, s.AppendResult(id, mutationWarningResult(id)))
				}
				receipt = mutationWarningReceipt(t, s, p, id)
				var err error
				before, err = os.ReadFile(receipt)
				require.NoError(t, err)
				body, err := yaml.Marshal(mutationWarningResult(id))
				require.NoError(t, err)
				args["result"] = string(body)
			}
			r, err := tool.Handler(context.Background(), callTool(args))
			require.NoError(t, err)
			require.False(t, r.IsError, resultText(t, r))
			var payload struct {
				Mission  string                  `json:"mission_id"`
				Warnings []string                `json:"warnings"`
				Outcome  mission.OperationResult `json:"outcome"`
			}
			require.NoError(t, json.Unmarshal([]byte(resultText(t, r)), &payload))
			require.NotEmpty(t, payload.Warnings, "handler propagation is part of this regression check")
			display := formatRegisteredMutation(t, "mission", method, r).Output
			assert.Contains(t, display.Summary, payload.Mission)
			assert.NotContains(t, display.Summary, "error:")
			assert.Equal(t, 1, strings.Count(display.Context, "Warnings:"))
			for _, warning := range payload.Warnings {
				assert.Contains(t, display.Context, "\n  - "+warning)
				if method == "reflect" {
					// No JSON escapes may disguise the duplicated occurrence in
					// the old outcome rendering. Count the literal diagnostic,
					// not just the common section's heading.
					encoded, err := json.Marshal(warning)
					require.NoError(t, err)
					require.Equal(t, `"`+warning+`"`, string(encoded))
					assert.Equal(t, 1, strings.Count(display.Context, warning))
				}
			}
			if method == "reflect" {
				outcomeContext, _, found := strings.Cut(display.Context, "\n\nWarnings:")
				require.True(t, found)
				var rendered mission.OperationResult
				require.NoError(t, json.Unmarshal([]byte(outcomeContext), &rendered))
				assert.Equal(t, payload.Outcome.Application, rendered.Application)
				assert.Equal(t, payload.Outcome.Durability, rendered.Durability)
				assert.Equal(t, payload.Outcome.Trace, rendered.Trace)
				assert.Empty(t, rendered.Diagnostics, "overlaps belong only in the common warning section")
			}
			if method != "create" {
				assert.Contains(t, strings.Join(payload.Warnings, "\n"), "accepted; optional reflection trace pending")
			}
			if receipt != "" {
				after, err := os.ReadFile(receipt)
				require.NoError(t, err)
				assert.Equal(t, before, after, "rendering must not destroy interrupted recovery evidence")
			}
			c, err := s.Load(payload.Mission)
			require.NoError(t, err)
			switch method {
			case "result", "close":
				accepted, err := s.LoadResult(c.MissionID, 1)
				require.NoError(t, err)
				require.NotNil(t, accepted)
				if method == "close" {
					assert.Equal(t, mission.StatusClosed, c.Status)
				}
			case "advance":
				assert.Equal(t, 2, c.CurrentRound)
			case "abandon", "correct":
				assert.Equal(t, mission.StatusAbandoned, c.Status)
				if method == "correct" {
					corrections, _, err := s.LoadCorrections(c.MissionID)
					require.NoError(t, err)
					assert.Len(t, corrections, 1)
				}
			case "reflect":
				assert.Len(t, c.RoundDecisions.Entries, 2)
			case "create":
				assert.Equal(t, mission.StatusOpen, c.Status)
			}
		})
	}
}

func TestRegisteredMissionFormatWarningsConcurrent(t *testing.T) {
	s, p := mutationWarningStore(t)
	srv, _ := fullServer(t, WithMissionStore(s))
	tool := srv.GetTool("mission")
	const count = 8
	requests := make([]mcplib.CallToolRequest, count)
	ids := make([]string, count)
	for i := range count {
		id := fmt.Sprintf("m-2026-11-05-%03d", 200+i)
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
		index     int
		r         *mcplib.CallToolResult
		formatted []byte
		err       error
	}
	answers := make(chan answer, count)
	start := make(chan struct{})
	for i, req := range requests {
		go func() {
			<-start
			r, err := tool.Handler(context.Background(), req)
			var out bytes.Buffer
			if err == nil {
				var data []byte
				data, err = mutationFormatPayload("mission", "result", r)
				if err == nil {
					err = hook.HandleFormatOutput(bytes.NewReader(data), &out)
				}
			}
			answers <- answer{i, r, out.Bytes(), err}
		}()
	}
	close(start)
	for range count {
		a := <-answers
		require.NoError(t, a.err)
		require.False(t, a.r.IsError, resultText(t, a.r))
		var formatted missionWarningDisplay
		require.NoError(t, json.Unmarshal(a.formatted, &formatted))
		display := formatted.Output
		assert.Equal(t, "Result "+ids[a.index]+" round 1 (pass)", display.Summary)
		if a.index%2 == 0 {
			assert.Contains(t, display.Context, ids[a.index]+" round 1 accepted; optional reflection trace pending")
		} else {
			assert.Empty(t, display.Context, "warning-free callers must not inherit another request's warning")
		}
	}
}

func TestRegisteredMissionFormatWarningsRefused(t *testing.T) {
	for _, method := range []string{"result", "advance", "close", "abandon", "reflect", "correct"} {
		t.Run(method, func(t *testing.T) {
			s, p := mutationWarningStore(t)
			srv, identities := fullServer(t, WithMissionStore(s))
			recoveryWarningAuthor(t, identities)
			id := "m-2026-11-05-198"
			var req mcplib.CallToolRequest
			if method == "reflect" || method == "correct" {
				req = recoveryWarningRequest(t, s, p, id, method, true, true)
			} else {
				mutationWarningMission(t, s, id)
				mutationWarningReceipt(t, s, p, id)
				if method == "advance" {
					_, _, err := s.AdvanceRoundWithOutcome(id, "lead")
					require.NoError(t, err)
				}
				req = callTool(map[string]interface{}{"method": method, "mission_id": id})
			}
			r, err := srv.GetTool("mission").Handler(context.Background(), req)
			require.NoError(t, err)
			require.True(t, r.IsError, resultText(t, r))
			formatRegisteredMutation(t, "mission", method, r)
		})
	}
}

// Vendor is the other mutation with a warnings field, but its schema is an
// array of credential-key findings, not mission operation-diagnostic strings.
// Exercise a real accepted apply and its existing dedicated formatter so the
// common mission fix cannot accidentally reinterpret or expose extension data.
func TestRegisteredVendorFormatWarnings(t *testing.T) {
	root := t.TempDir()
	identities := identity.NewStore(root)
	recoveryWarningAuthor(t, identities)
	require.NoError(t, identities.ExtSet("author", "quarry", "api_token", "fixture-extension-value"))
	dest := t.TempDir()
	sources := vendor.Sources{
		Roots: []string{root}, Identities: identities,
		Roles: role.NewLayeredStore("", root), Teams: team.NewLayeredStore("", root),
	}
	srv, _ := fullServer(t, WithVendorRunner(func(opts vendor.Options) (*vendor.Plan, error) {
		v, err := vendor.New(sources, opts)
		if err != nil {
			return nil, err
		}
		return v.Run()
	}))
	r, err := srv.GetTool("vendor").Handler(context.Background(), callTool(map[string]interface{}{
		"handles": []interface{}{"author"}, "apply": true, "to": dest,
		"allow_ext_key": []interface{}{"quarry/api_token"},
	}))
	require.NoError(t, err)
	require.False(t, r.IsError, resultText(t, r))
	var plan vendor.Plan
	require.NoError(t, json.Unmarshal([]byte(resultText(t, r)), &plan))
	require.True(t, plan.Applied)
	require.Len(t, plan.Warnings, 1)
	display := formatRegisteredMutation(t, "vendor", "", r).Output
	assert.Contains(t, display.Summary, "Vendored 1 identities")
	assert.Contains(t, display.Context, "Extension keys worth a look: api_token")
	assert.NotContains(t, display.Context, "fixture-extension-value")
	require.NoError(t, vendor.Verify(dest))
}
