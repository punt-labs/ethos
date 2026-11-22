//go:build !windows

package diagnostic

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/punt-labs/punt-observe-go/telemetry"
)

// Command is a canonical command path from the compiled command tree.
type Command string

// Tool and Action name members of the compiled MCP tool schemas.
type Tool string
type Action string

// Invocation describes a completed CLI, hook or UI operation without its inputs.
type Invocation struct {
	Command  Command
	ExitCode int
	Duration time.Duration
}

// ToolCall describes a completed MCP call without its arguments or result.
type ToolCall struct {
	Tool     Tool
	Action   Action
	Failed   bool
	Duration time.Duration
}

// Usage owns local-only capture. It never configures a network exporter.
type Usage struct{ client *telemetry.Client }

// Close bounds the best-effort flush without changing the command result.
func (u *Usage) Close() {
	if u == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	u.client.Shutdown(ctx)
}

const (
	completed telemetry.EnumMember = "completed"
	failed    telemetry.EnumMember = "failed"
)

func commandMember(command Command) telemetry.EnumMember {
	return telemetry.EnumMember(strings.ReplaceAll(string(command), " ", "-"))
}

// NewUsage compiles closed command enums from the binary's command tree.
func NewUsage(logger *slog.Logger, commands []Command) *Usage {
	members := make([]telemetry.EnumMember, 0, len(commands))
	for _, command := range commands {
		members = append(members, commandMember(command))
	}
	common := func() map[string]telemetry.Field {
		return map[string]telemetry.Field{
			"outcome":     {Kind: telemetry.Enum, Members: []telemetry.EnumMember{completed, failed}, Required: true},
			"duration_ms": {Kind: telemetry.Duration, Required: true},
		}
	}
	var schemas []telemetry.Schema
	for _, surface := range []string{"cli", "hook", "ui"} {
		fields := common()
		key := "command"
		if surface == "hook" {
			key = "event"
		}
		fields[key] = telemetry.Field{Kind: telemetry.Enum, Members: members, Required: true}
		fields["exit_code"] = telemetry.Field{Kind: telemetry.Integer, Required: true}
		schemas = append(schemas, telemetry.Schema{Name: "ethos." + surface + ".invoke", Version: 1, Signal: telemetry.Event, Fields: fields})
	}
	fields := common()
	fields["tool"] = telemetry.Field{Kind: telemetry.Enum, Required: true, Members: []telemetry.EnumMember{
		"unknown", "identity", "talent", "personality", "writing_style", "role", "team", "adr", "ext", "session", "mission", "vendor", "doctor",
	}}
	fields["action"] = telemetry.Field{Kind: telemetry.Enum, Members: []telemetry.EnumMember{
		"whoami", "get", "create", "list", "show", "delete", "add", "remove", "set", "del", "add_member", "remove_member", "add_collab", "for_repo", "roster", "join", "leave", "iam", "close", "abandon", "reflect", "reflections", "advance", "result", "results", "log", "correct",
	}}
	schemas = append(schemas, telemetry.Schema{Name: "ethos.mcp.call", Version: 1, Signal: telemetry.Event, Fields: fields})
	return &Usage{client: telemetry.New("ethos", telemetry.Config{Diagnostics: logger}, schemas...)}
}

func usageOutcome(isFailed bool) telemetry.EnumMember {
	if isFailed {
		return failed
	}
	return completed
}

// Invocation records only a validated command enum and its outcome metadata.
func (u *Usage) Invocation(surface string, call Invocation) {
	if u == nil {
		return
	}
	key := "command"
	if surface == "hook" {
		key = "event"
	}
	u.client.Record(telemetry.Observation{Name: "ethos." + surface + ".invoke", Version: 1, Attributes: []telemetry.Value{
		telemetry.EnumValue(key, commandMember(call.Command)),
		telemetry.EnumValue("outcome", usageOutcome(call.ExitCode != 0)),
		telemetry.Int("exit_code", int64(call.ExitCode)),
		telemetry.Elapsed("duration_ms", call.Duration),
	}})
}

// ToolCall records only schema-validated tool and action enums.
func (u *Usage) ToolCall(call ToolCall) {
	if u == nil {
		return
	}
	attrs := []telemetry.Value{
		telemetry.EnumValue("tool", telemetry.EnumMember(call.Tool)),
		telemetry.EnumValue("outcome", usageOutcome(call.Failed)),
		telemetry.Elapsed("duration_ms", call.Duration),
	}
	if call.Action != "" {
		attrs = append(attrs, telemetry.EnumValue("action", telemetry.EnumMember(call.Action)))
	}
	u.client.Record(telemetry.Observation{Name: "ethos.mcp.call", Version: 1, Attributes: attrs})
}
