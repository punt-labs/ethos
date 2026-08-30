package mcp

import (
	"context"

	"github.com/punt-labs/ethos/v4/internal/vendor"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// VendorRunner is the vendor operation the tool drives. The CLI supplies
// the concrete implementation, because building it needs the repo roots
// and layered stores that only the command layer resolves.
type VendorRunner func(vendor.Options) (*vendor.Plan, error)

// WithVendorRunner enables the vendor tool.
func WithVendorRunner(run VendorRunner) Option {
	return func(h *Handler) { h.vendor = run }
}

func (h *Handler) vendorTool() mcplib.Tool {
	return mcplib.NewTool("vendor",
		mcplib.WithDescription(
			"Snapshot a complete, self-standing identity set into this repo's .punt-labs/ethos/. "+
				"Follows references to a fixed point — attributes, roles, and the teams an identity "+
				"belongs to, plus those teams' other members — so the result resolves with no global "+
				"ethos store. Plans by default; set apply=true to write."),
		mcplib.WithArray("handles",
			mcplib.Description("Identity handles to seed the closure from."),
			mcplib.Items(map[string]any{"type": "string"}),
		),
		mcplib.WithString("team",
			mcplib.Description("Seed the closure from this team's members instead of named handles."),
		),
		mcplib.WithBoolean("all",
			mcplib.Description("Seed the closure from every readable identity."),
		),
		mcplib.WithString("to",
			mcplib.Description("Destination root. Defaults to this repo's .punt-labs/ethos."),
		),
		mcplib.WithBoolean("prune",
			mcplib.Description("Remove vendored files the closure no longer contains."),
		),
		mcplib.WithBoolean("apply",
			mcplib.Description("Write the snapshot. Without it the call only plans — vendor writes into git-tracked space."),
		),
		mcplib.WithArray("allow_ext_key",
			mcplib.Description("Allow one credential-named extension key, as <namespace>/<key>. Per key only; there is no blanket override."),
			mcplib.Items(map[string]any{"type": "string"}),
		),
	)
}

func (h *Handler) handleVendor(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.vendor == nil {
		return mcplib.NewToolResultError("vendor is not available on this server"), nil
	}
	handles, err := stringListArg(req, "handles")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	allowExtKey, err := stringListArg(req, "allow_ext_key")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	team, err := stringArgStrict(req, "team")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	to, err := stringArgStrict(req, "to")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	plan, err := h.vendor(vendor.Options{
		Handles:      handles,
		Team:         team,
		All:          boolArg(req, "all", false),
		Dest:         to,
		Prune:        boolArg(req, "prune", false),
		Apply:        boolArg(req, "apply", false),
		AllowExtKeys: allowExtKey,
	})
	if err != nil {
		// The credential refusal and the incompleteness report are both
		// fully-formed diagnostics naming what to fix; surface them
		// verbatim rather than wrapping them in a generic failure.
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if plan == nil {
		return mcplib.NewToolResultError("vendor returned no plan"), nil
	}
	return jsonResult(plan)
}
