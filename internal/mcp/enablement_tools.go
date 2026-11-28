package mcp

import (
	"context"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

func (h *Handler) registerEnablementTools(s *mcpserver.MCPServer) {
	scope := mcplib.WithString("scope", mcplib.Enum("project", "local"), mcplib.Description("Integration scope; defaults to local."))
	h.addLocatedTool(s, mcplib.NewTool("enable", mcplib.WithDescription("Enable Ethos in the current checkout."),
		scope, mcplib.WithBoolean("clear_opt_out", mcplib.Description("Clear the committed opt-out (project only)."))), changeEnablement)
	h.addLocatedTool(s, mcplib.NewTool("disable", mcplib.WithDescription("Disable Ethos in the current checkout."),
		scope, mcplib.WithBoolean("opt_out", mcplib.Description("Record a committed opt-out (project only)."))), changeEnablement)
	h.addLocatedTool(s, mcplib.NewTool("status", mcplib.WithDescription("Show the current checkout's effective Ethos state.")), enablementStatus)
}

func changeEnablement(_ *Handler, _ context.Context, req mcplib.CallToolRequest, loc StoreLocation) (*mcplib.CallToolResult, error) {
	if loc.Checkout == "" {
		return mcplib.NewToolResultError("not in a git checkout"), nil
	}
	scope, err := stringArgStrict(req, "scope")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if scope != "" && scope != "local" && scope != "project" {
		return mcplib.NewToolResultError("scope must be project or local"), nil
	}
	enabling := req.Params.Name == "enable"
	optOut := boolArg(req, "opt_out", false)
	clear := boolArg(req, "clear_opt_out", false)
	if scope != "project" && (optOut || clear) {
		return mcplib.NewToolResultError("opt_out and clear_opt_out require project scope"), nil
	}
	switch {
	case scope != "project" && enabling:
		_, err = enable.EnableLocal(loc.Checkout, false)
	case scope != "project":
		_, err = enable.DisableLocal(loc.Checkout)
	case enabling:
		root := loc.Root
		if root == "" {
			root, err = enable.StoreRoot(loc.Checkout, root, loc.Err)
		}
		if err == nil {
			_, err = enable.EnableTo(loc.Checkout, root, enable.Options{ClearOptOut: clear})
		}
	default:
		_, err = enable.DisableTo(loc.Checkout, loc.Root, false, optOut)
	}
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	return enablementStatus(nil, nil, req, loc)
}

func enablementStatus(_ *Handler, _ context.Context, _ mcplib.CallToolRequest, loc StoreLocation) (*mcplib.CallToolResult, error) {
	state, err := resolve.EffectiveEnablement(loc.Checkout)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	return mcplib.NewToolResultText(state.String()), nil
}
