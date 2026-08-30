package mcp

import (
	"github.com/punt-labs/ethos/v4/internal/schema"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// schemaOptions turns an entity's registry fields into MCP tool options,
// one per field. It is the single generator behind every create tool's
// field set, so the advertised schema and the handler that reads it cannot
// name different fields.
//
// The type mapping mirrors the registry:
//   - list of object → a bare array; its object shape lives in the
//     description string, matching the mcp-go array builder's item support.
//   - list of scalar → an array of strings.
//   - closed enum → a string constrained to the enum values.
//   - everything else (including the partial role.model enum) → a plain
//     string; its legal values are spelled out in the description.
func schemaOptions(e schema.Entity) []mcplib.ToolOption {
	var opts []mcplib.ToolOption
	for _, f := range e.Fields() {
		switch {
		case len(f.Fields) > 0:
			opts = append(opts, mcplib.WithArray(f.Name, mcplib.Description(f.Description)))
		case f.List:
			opts = append(opts, mcplib.WithArray(f.Name,
				mcplib.Description(f.Description), mcplib.WithStringItems()))
		case len(f.Enum) > 0 && f.Pattern == "":
			opts = append(opts, mcplib.WithString(f.Name,
				mcplib.Description(f.Description), mcplib.Enum(f.Enum...)))
		default:
			opts = append(opts, mcplib.WithString(f.Name, mcplib.Description(f.Description)))
		}
	}
	return opts
}

// withOptions prepends the fixed method-dispatch options to the generated
// field options, producing the full option list for NewTool.
func withOptions(fixed []mcplib.ToolOption, e schema.Entity) []mcplib.ToolOption {
	return append(fixed, schemaOptions(e)...)
}
