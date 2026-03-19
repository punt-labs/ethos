#!/usr/bin/env bash
# hooks/suppress-output.sh — Format MCP tool output for ethos
set -eo pipefail

# Read tool response from stdin
INPUT=$(cat)

TOOL_NAME=$(printf '%s' "$INPUT" | grep -o '"tool_name":"[^"]*"' | head -1 | cut -d'"' -f4 2>/dev/null || true)

# Never suppress error responses — pass them through unchanged
if printf '%s' "$INPUT" | grep -q '"is_error" *: *true'; then
  exit 0
fi

# Format based on tool
case "$TOOL_NAME" in
  *whoami*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Identity resolved."}}'
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
  *ext_get*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Extension loaded."}}'
    ;;
  *ext_set*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Extension updated."}}'
    ;;
  *ext_del*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Extension deleted."}}'
    ;;
  *ext_list*)
    printf '{"hookSpecificOutput":{"updatedMCPToolOutput":"Extensions listed."}}'
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
