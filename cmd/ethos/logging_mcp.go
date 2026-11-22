package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
)

func toolLoggingHooks(logger *slog.Logger, lookup func(string) *server.ServerTool) *server.Hooks {
	hooks := &server.Hooks{}
	var starts sync.Map
	hooks.AddBeforeCallTool(func(_ context.Context, _ any, req *mcp.CallToolRequest) {
		starts.Store(req, time.Now())
	})
	finish := func(req *mcp.CallToolRequest, result any, err error) {
		started := time.Now()
		if stamp, ok := starts.LoadAndDelete(req); ok {
			started = stamp.(time.Time)
		}
		outcome := "completed"
		if r, ok := result.(*mcp.CallToolResult); err != nil || (ok && r != nil && r.IsError) {
			outcome = "failed"
		}
		name := "unknown"
		action := ""
		attrs := []any{}
		if tool := lookup(req.Params.Name); tool != nil {
			name = tool.Tool.Name
			for _, key := range []string{"action", "method"} {
				property, _ := tool.Tool.InputSchema.Properties[key].(map[string]any)
				values, _ := property["enum"].([]string)
				value, _ := req.GetArguments()[key].(string)
				for _, allowed := range values {
					if value == allowed {
						action = allowed
						attrs = append(attrs, "action", allowed)
					}
				}
			}
		}
		attrs = append(attrs, "tool", name, "outcome", outcome, "duration_ms", time.Since(started).Milliseconds())
		if err != nil {
			attrs = append(attrs, "error_type", diagnostic.ErrorType(err))
		}
		logger.Info("Tool call finished", attrs...)
		processUsage.ToolCall(diagnostic.ToolCall{Tool: diagnostic.Tool(name), Action: diagnostic.Action(action), Failed: outcome == "failed", Duration: time.Since(started)})
	}
	hooks.AddAfterCallTool(func(_ context.Context, _ any, req *mcp.CallToolRequest, result any) {
		finish(req, result, nil)
	})
	hooks.AddOnError(func(_ context.Context, _ any, method mcp.MCPMethod, message any, err error) {
		if req, ok := message.(*mcp.CallToolRequest); ok && method == mcp.MethodToolsCall {
			finish(req, nil, err)
		}
	})
	return hooks
}
