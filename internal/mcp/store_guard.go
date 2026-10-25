package mcp

import (
	"context"
	"fmt"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// WithStoreError tells the handler the repository store could not be located
// (an unreadable .git, a refused ETHOS_REPO_ROOT). Tool methods that write the
// store then refuse, and the others say the answer is not the repository's.
func WithStoreError(err error) Option {
	return func(h *Handler) { h.storeErr = err }
}

// storeWrites lists, per tool, the methods that write the repository store. An
// empty method stands for a tool with no methods, all of whose calls write.
var storeWrites = map[string][]string{
	"identity":      {"create"},
	"ext":           {"set", "del"},
	"talent":        {"create", "delete", "add", "remove", "set"},
	"personality":   {"create", "delete", "set"},
	"writing_style": {"create", "delete", "set"},
	"role":          {"create", "delete"},
	"team":          {"create", "delete", "add_member", "remove_member", "add_collab"},
	"mission":       {"create", "close", "abandon", "reflect", "advance", "result", "correct"},
	"vendor":        {""},
}

// addTool registers a tool, guarded when the store could not be located.
func (h *Handler) addTool(s *mcpserver.MCPServer, tool mcplib.Tool, fn mcpserver.ToolHandlerFunc) {
	s.AddTool(tool, h.guardStore(tool.Name, fn))
}

// guardStore wraps fn: a write method refuses with the reason the store could
// not be located; any other result carries a note saying so.
func (h *Handler) guardStore(tool string, fn mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	if h.storeErr == nil {
		return fn
	}
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		method := stringArg(req, "method", "")
		for _, w := range storeWrites[tool] {
			if w == method {
				return mcplib.NewToolResultError(fmt.Sprintf("%s %s: %v; refusing to write the global store instead",
					tool, method, h.storeErr)), nil
			}
		}
		result, err := fn(ctx, req)
		if err != nil || result == nil || len(result.Content) == 0 {
			return result, err
		}
		if tc, ok := result.Content[0].(mcplib.TextContent); ok {
			tc.Text = fmt.Sprintf("note: the repository store could not be located (%v); this answer is from the global store only\n\n%s",
				h.storeErr, tc.Text)
			result.Content[0] = tc
		}
		return result, nil
	}
}
