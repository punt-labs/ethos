package mcp

import (
	"context"
	"fmt"
	"slices"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// WithStoreError tells the handler the repository store could not be located
// (a .git git refuses, a refused ETHOS_REPO_ROOT). Only the tool methods
// declared reads then run, each with a note that its answer is from the global
// store only; every other method refuses.
func WithStoreError(err error) Option {
	return func(h *Handler) { h.storeErr = err }
}

// unguardedTools never touch the repository store: session and adr keep
// global-only stores, and doctor reports on the store's state. The matching CLI
// commands are exempt from the store refusal too.
var unguardedTools = map[string]bool{"session": true, "doctor": true, "adr": true}

// storeReads lists, per guarded tool, the methods that only read. They are the
// only methods that run when the store cannot be located: anything not listed
// here, including a method added later, refuses.
var storeReads = map[string][]string{
	"identity":      {"whoami", "list", "get"},
	"ext":           {"get", "list"},
	"talent":        {"list", "show"},
	"personality":   {"list", "show"},
	"writing_style": {"list", "show"},
	"role":          {"list", "show"},
	"team":          {"list", "show", "for_repo"},
	"mission":       {"show", "list", "reflections", "results", "log"},
}

// storeWrites lists, per guarded tool, the methods that write the store. The
// guard does not consult it — it denies everything that is not a read — but a
// test requires every method to be classified exactly once. An empty method
// stands for a tool with no methods, all of whose calls write.
var storeWrites = map[string][]string{
	"identity":      {"create"},
	"ext":           {"set", "del"},
	"talent":        {"create", "delete", "add", "remove"},
	"personality":   {"create", "delete", "set"},
	"writing_style": {"create", "delete", "set"},
	"role":          {"create", "delete"},
	"team":          {"create", "delete", "add_member", "remove_member", "add_collab"},
	"mission":       {"create", "close", "abandon", "reflect", "advance", "result", "correct"},
	"vendor":        {""},
}

// addTool registers every tool. A tool not declared unguarded is guarded.
func (h *Handler) addTool(s *mcpserver.MCPServer, tool mcplib.Tool, fn mcpserver.ToolHandlerFunc) {
	if unguardedTools[tool.Name] {
		s.AddTool(tool, fn)
		return
	}
	s.AddTool(tool, h.guardStore(tool.Name, fn))
}

// guardStore wraps fn: when the store could not be located, a declared read
// runs and its result gains a second text block saying so (the first block,
// the tool's own output, is left untouched for formatters and JSON parsers);
// any other method refuses with the reason.
func (h *Handler) guardStore(tool string, fn mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	if h.storeErr == nil {
		return fn
	}
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		method := stringArg(req, "method", "")
		if !slices.Contains(storeReads[tool], method) {
			return mcplib.NewToolResultError(fmt.Sprintf(
				"%s %s: %v; refusing: only reads run when the repository store cannot be located, never against the global store instead",
				tool, method, h.storeErr)), nil
		}
		result, err := fn(ctx, req)
		if err != nil || result == nil {
			return result, err
		}
		result.Content = append(result.Content, mcplib.NewTextContent(fmt.Sprintf(
			"note: the repository store could not be located (%v); this answer is from the global store only", h.storeErr)))
		return result, nil
	}
}
