#!/usr/bin/env bash
# hooks/suppress-output.sh — Format MCP tool output for ethos
set -euo pipefail

# Read tool response from stdin
INPUT=$(cat)

TOOL_NAME=$(printf '%s' "$INPUT" | grep -o '"tool_name":"[^"]*"' | head -1 | cut -d'"' -f4 2>/dev/null || true)

# Format based on tool
case "$TOOL_NAME" in
  *whoami*)
    RESULT=$(printf '%s' "$INPUT" | grep -o '"tool_response":{[^}]*}' | head -1 || true)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Identity resolved."}}'
    ;;
  *)
    # Default: suppress raw JSON
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Done."}}'
    ;;
esac
