package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/punt-labs/ethos/internal/adr"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// adrTool defines the consolidated `adr` MCP tool.
func (h *Handler) adrTool() mcplib.Tool {
	return mcplib.NewTool("adr",
		mcplib.WithDescription("Manage architecture decision records. Methods: create, list, show."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("create", "list", "show"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("id",
			mcplib.Description("ADR ID (e.g. DES-042). Required for show."),
		),
		mcplib.WithString("title",
			mcplib.Description("ADR title. Required for create."),
		),
		mcplib.WithString("context_text",
			mcplib.Description("What prompted the decision. For create."),
		),
		mcplib.WithString("decision",
			mcplib.Description("What was decided. Required for create."),
		),
		mcplib.WithString("status",
			mcplib.Description("Filter for list (proposed|settled|superseded|all) or initial status for create."),
		),
		mcplib.WithString("author",
			mcplib.Description("Author handle. For create."),
		),
		mcplib.WithString("mission_id",
			mcplib.Description("Link to mission. For create."),
		),
		mcplib.WithString("bead_id",
			mcplib.Description("Link to bead. For create."),
		),
	)
}

// handleADR dispatches on the method argument.
func (h *Handler) handleADR(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.adrStore == nil {
		return mcplib.NewToolResultError("ADR store not configured"), nil
	}
	method := stringArg(req, "method", "")
	switch method {
	case "create":
		return h.handleCreateADR(req)
	case "list":
		return h.handleListADRs(req)
	case "show":
		return h.handleShowADR(req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

func (h *Handler) handleCreateADR(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	title := stringArg(req, "title", "")
	if strings.TrimSpace(title) == "" {
		return mcplib.NewToolResultError("title is required for create"), nil
	}
	decision := stringArg(req, "decision", "")
	if strings.TrimSpace(decision) == "" {
		return mcplib.NewToolResultError("decision is required for create"), nil
	}

	a := &adr.ADR{
		Title:     title,
		Status:    stringArg(req, "status", adr.StatusProposed),
		Author:    stringArg(req, "author", ""),
		Context:   stringArg(req, "context_text", ""),
		Decision:  decision,
		MissionID: stringArg(req, "mission_id", ""),
		BeadID:    stringArg(req, "bead_id", ""),
	}

	if err := h.adrStore.Create(a); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create ADR: %v", err)), nil
	}
	return jsonResult(a)
}

func (h *Handler) handleListADRs(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	statusFilter := stringArg(req, "status", "all")
	if err := adr.ValidateStatusFilter(statusFilter); err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	ids, err := h.adrStore.List()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list ADRs: %v", err)), nil
	}

	type entry struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
		Author string `json:"author,omitempty"`
	}

	var entries []entry
	for _, id := range ids {
		a, err := h.adrStore.Load(id)
		if err != nil {
			continue
		}
		if statusFilter != "all" && a.Status != statusFilter {
			continue
		}
		entries = append(entries, entry{
			ID:     a.ID,
			Title:  a.Title,
			Status: a.Status,
			Author: a.Author,
		})
	}
	if entries == nil {
		entries = []entry{}
	}
	return jsonResult(entries)
}

func (h *Handler) handleShowADR(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	id := stringArg(req, "id", "")
	if strings.TrimSpace(id) == "" {
		return mcplib.NewToolResultError("id is required for show"), nil
	}
	a, err := h.adrStore.Load(id)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("ADR not found: %v", err)), nil
	}
	return jsonResult(a)
}
