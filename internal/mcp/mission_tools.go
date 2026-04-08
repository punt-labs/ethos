package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/mission"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// missionTool defines the consolidated `mission` MCP tool. The single
// tool dispatches on the `method` enum so callers see one entry point in
// their MCP server's tool list, mirroring how the team and identity
// tools are exposed.
func (h *Handler) missionTool() mcplib.Tool {
	return mcplib.NewTool("mission",
		mcplib.WithDescription("Manage mission contracts (typed delegation artifacts). Methods: create, show, list, close. Create resolves the evaluator handle and pins a content hash; verifier spawns are refused if the content has drifted."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("create", "show", "list", "close"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("mission_id",
			mcplib.Description("Mission ID or unique prefix. Required for show and close."),
		),
		mcplib.WithString("contract",
			mcplib.Description("Full contract YAML body. Required for create."),
		),
		mcplib.WithString("status",
			// No enum constraint: the valid values differ per method
			// (list accepts "open|closed|failed|escalated|all",
			// close accepts "closed|failed|escalated" only). A shared
			// enum would advertise "open" and "all" as valid for close,
			// which is wrong. Each handler validates its own input.
			mcplib.Description("Filter for list (open|closed|failed|escalated|all) or terminal status for close (closed|failed|escalated)."),
		),
	)
}

// handleMission dispatches on the method argument to the per-method
// handlers. Unknown methods return a tool error result rather than a Go
// error so the MCP client sees a structured failure.
func (h *Handler) handleMission(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.missionStore == nil {
		return mcplib.NewToolResultError("mission store not configured"), nil
	}
	method := stringArg(req, "method", "")
	switch method {
	case "create":
		return h.handleCreateMission(req)
	case "show":
		return h.handleShowMission(req)
	case "list":
		return h.handleListMissions(req)
	case "close":
		return h.handleCloseMission(req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

// handleCreateMission parses the contract YAML, fills in
// server-controlled fields, and persists. The contract argument is the
// trust boundary; we use yaml.v3's KnownFields(true) so unrecognized
// keys are an error rather than silently dropped.
func (h *Handler) handleCreateMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	body := stringArg(req, "contract", "")
	if strings.TrimSpace(body) == "" {
		return mcplib.NewToolResultError("contract YAML body is required for create"), nil
	}

	// Strict decode via the shared helper: unknown fields, multi-doc
	// YAML, and trailing content are all rejected. CLI and MCP share
	// this entry point so the input trust boundary is enforced
	// identically regardless of how the YAML reached the store.
	parsed, err := mission.DecodeContractStrict([]byte(body), "mcp create request")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	c := *parsed

	// Apply server-controlled fields (mission_id, status, timestamps,
	// evaluator.pinned_at, evaluator.hash) via the shared helper.
	// CLI and MCP entry points are in lockstep: any caller-supplied
	// values for these fields are overwritten identically regardless
	// of where the YAML came from. Hash sources resolve the evaluator
	// against the live identity, role, and team stores; an
	// unresolvable handle is fatal — see DES-033.
	//
	// NewLiveHashSources rejects nil role or team stores so an MCP
	// handler built without WithRoleStore/WithTeamStore fails loudly
	// here instead of silently pinning a role-free hash that the
	// verifier hook (always wired with both stores) could never match.
	sources, err := mission.NewLiveHashSources(h.store, h.roles, h.teams)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if err := h.missionStore.ApplyServerFields(&c, time.Now(), sources); err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	if err := h.missionStore.Create(&c); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create mission: %v", err)), nil
	}
	return jsonResult(&c)
}

// handleShowMission resolves the requested mission by exact ID or
// prefix and returns its contract.
func (h *Handler) handleShowMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for show"), nil
	}
	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	c, err := h.missionStore.Load(id)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to load mission: %v", err)), nil
	}
	return jsonResult(c)
}

// handleListMissions returns the missions matching the status filter.
// The default filter is "open" so callers see their pending work, not
// closed historical records.
func (h *Handler) handleListMissions(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	status := stringArg(req, "status", "open")
	if !mission.IsValidStatusFilter(status) {
		return mcplib.NewToolResultError(fmt.Sprintf(
			"invalid status filter %q: must be one of open, closed, failed, escalated, all",
			status,
		)), nil
	}
	ids, err := h.missionStore.List()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list missions: %v", err)), nil
	}

	entries := []mission.ListEntry{}
	for _, id := range ids {
		c, loadErr := h.missionStore.Load(id)
		if loadErr != nil {
			// Skip corrupt rows; the CLI will surface them.
			continue
		}
		if !mission.StatusMatches(status, c.Status) {
			continue
		}
		entries = append(entries, mission.NewListEntry(c))
	}
	return jsonResult(entries)
}

// handleCloseMission resolves the mission by ID or prefix and applies
// the requested terminal status. Defaults to StatusClosed when no status
// argument is supplied.
func (h *Handler) handleCloseMission(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	idArg := stringArg(req, "mission_id", "")
	if idArg == "" {
		return mcplib.NewToolResultError("mission_id is required for close"), nil
	}
	status := stringArg(req, "status", mission.StatusClosed)

	id, err := h.missionStore.MatchByPrefix(idArg)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if err := h.missionStore.Close(id, status); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to close mission: %v", err)), nil
	}
	return jsonResult(map[string]string{
		"mission_id": id,
		"status":     status,
	})
}

