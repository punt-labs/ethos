#!/usr/bin/env bash
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
# Format ethos MCP tool output for the UI panel.
# No `set -euo pipefail` — hooks must degrade gracefully on
# malformed input rather than failing the tool call.

# Require jq for JSON processing — degrade to no-op if missing
command -v jq &>/dev/null || exit 0

INPUT=$(cat)
TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null)
TOOL_NAME="${TOOL##*__}"

# No-op if we couldn't parse the tool name
[[ -n "$TOOL_NAME" ]] || exit 0

# Never suppress error responses — pass them through unchanged
IS_ERROR=$(printf '%s' "$INPUT" | jq -r '.tool_response | if type == "array" then .[0].is_error // false else .is_error // false end' 2>/dev/null)
if [[ "$IS_ERROR" == "true" ]]; then
  exit 0
fi

emit() {
  local summary="$1"
  jq -n --arg summary "$summary" '{
    hookSpecificOutput: {
      hookEventName: "PostToolUse",
      updatedMCPToolOutput: $summary
    }
  }'
}

case "$TOOL_NAME" in
  whoami)            emit "Identity resolved." ;;
  session_roster)    emit "Session roster loaded." ;;
  session_iam)       emit "Persona declared." ;;
  session_join)      emit "Participant joined." ;;
  session_leave)     emit "Participant left." ;;
  ext_get)           emit "Extension loaded." ;;
  ext_set)           emit "Extension updated." ;;
  ext_del)           emit "Extension deleted." ;;
  ext_list)          emit "Extensions listed." ;;
  list_identities)   emit "Identities listed." ;;
  get_identity)      emit "Identity loaded." ;;
  create_identity)   emit "Identity created." ;;
  *)                 emit "Done." ;;
esac

exit 0
