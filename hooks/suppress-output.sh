#!/usr/bin/env bash
# hooks/suppress-output.sh — Format MCP tool output for ethos
set -euo pipefail

# Read tool response from stdin
INPUT=$(cat)

TOOL_NAME=$(printf '%s' "$INPUT" | grep -o '"tool_name":"[^"]*"' | head -1 | cut -d'"' -f4 2>/dev/null || true)
TOOL_RESULT=$(printf '%s' "$INPUT" | grep -o '"tool_result":"[^"]*"' | head -1 | cut -d'"' -f4 2>/dev/null || true)

# Format based on tool
case "$TOOL_NAME" in
  *whoami*)
    if [[ -n "$TOOL_RESULT" ]]; then
      printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Identity: %s"}}' "$TOOL_RESULT"
    else
      printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Identity resolved."}}'
    fi
    ;;
  *session_roster*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Session roster loaded."}}'
    ;;
  *session_iam*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Persona declared."}}'
    ;;
  *session_join*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Participant joined."}}'
    ;;
  *session_leave*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Participant left."}}'
    ;;
  *ext_get*|*ext_set*|*ext_del*|*ext_list*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Extension updated."}}'
    ;;
  *list_identities*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Identities listed."}}'
    ;;
  *get_identity*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Identity loaded."}}'
    ;;
  *create_identity*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Identity created."}}'
    ;;
  *)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Done."}}'
    ;;
esac
