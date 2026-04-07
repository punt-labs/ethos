package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/mission"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"gopkg.in/yaml.v3"
)

// missionTool defines the consolidated `mission` MCP tool. The single
// tool dispatches on the `method` enum so callers see one entry point in
// their MCP server's tool list, mirroring how the team and identity
// tools are exposed.
func (h *Handler) missionTool() mcplib.Tool {
	return mcplib.NewTool("mission",
		mcplib.WithDescription("Manage mission contracts (typed delegation artifacts). Methods: create, show, list, close."),
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

	var c mission.Contract
	dec := yaml.NewDecoder(strings.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("parsing contract: %v", err)), nil
	}

	// Server-controlled fields. The caller may suggest a mission_id but
	// we always overwrite status, created_at, updated_at, and (when
	// missing) the evaluator's pinned_at.
	now := time.Now().UTC()
	if c.MissionID == "" {
		id, err := mission.NewID(h.missionStore.Root(), now)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("generating mission ID: %v", err)), nil
		}
		c.MissionID = id
	}
	c.Status = mission.StatusOpen
	c.CreatedAt = now.Format(time.RFC3339)
	c.UpdatedAt = c.CreatedAt
	if c.Evaluator.PinnedAt == "" {
		c.Evaluator.PinnedAt = c.CreatedAt
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
	ids, err := h.missionStore.List()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list missions: %v", err)), nil
	}

	type entry struct {
		MissionID string `json:"mission_id"`
		Status    string `json:"status"`
		Leader    string `json:"leader"`
		Worker    string `json:"worker"`
		Evaluator string `json:"evaluator"`
		CreatedAt string `json:"created_at"`
	}
	entries := []entry{}
	for _, id := range ids {
		c, loadErr := h.missionStore.Load(id)
		if loadErr != nil {
			// Skip corrupt rows; the CLI will surface them.
			continue
		}
		if !missionStatusMatches(status, c.Status) {
			continue
		}
		entries = append(entries, entry{
			MissionID: c.MissionID,
			Status:    c.Status,
			Leader:    c.Leader,
			Worker:    c.Worker,
			Evaluator: c.Evaluator.Handle,
			CreatedAt: c.CreatedAt,
		})
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

// missionStatusMatches mirrors the CLI's statusMatches helper. Kept
// inside the mcp package so the dispatch is independent of the CLI
// binary.
func missionStatusMatches(filter, contractStatus string) bool {
	if filter == "" || filter == "all" {
		return true
	}
	return filter == contractStatus
}
