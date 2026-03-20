#!/usr/bin/env bash
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
# Format ethos MCP tool output for the UI panel.
#
# Two-channel display (see punt-kit/patterns/two-channel-display.md):
#   updatedMCPToolOutput  -> compact panel line (max 80 cols)
#   additionalContext     -> full data for the model to reference
#
# No `set -euo pipefail` — hooks must degrade gracefully on
# malformed input rather than failing the tool call.

# Require jq for JSON processing — degrade to no-op if missing
command -v jq &>/dev/null || exit 0

INPUT=$(cat)
TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null)
TOOL_NAME="${TOOL##*__}"

# No-op if we couldn't parse the tool name
[[ -n "$TOOL_NAME" ]] || exit 0

# ── Functions (must be defined before use) ────────────────────────────

emit() {
  local summary="$1" ctx="$2"
  # Use --argjson for valid JSON, fall back to --arg for plain text
  if printf '%s' "$ctx" | jq empty 2>/dev/null; then
    jq -n --arg summary "$summary" --argjson ctx "$ctx" '{
      hookSpecificOutput: {
        hookEventName: "PostToolUse",
        updatedMCPToolOutput: $summary,
        additionalContext: ($ctx | tostring)
      }
    }'
  else
    jq -n --arg summary "$summary" --arg ctx "$ctx" '{
      hookSpecificOutput: {
        hookEventName: "PostToolUse",
        updatedMCPToolOutput: $summary,
        additionalContext: $ctx
      }
    }'
  fi
}

emit_simple() {
  local summary="$1"
  jq -n --arg summary "$summary" '{
    hookSpecificOutput: {
      hookEventName: "PostToolUse",
      updatedMCPToolOutput: $summary
    }
  }'
}

# ── Check MCP-level is_error flag first ───────────────────────────────
MCP_ERROR=$(printf '%s' "$INPUT" | jq -r '
  if (.tool_response | type) == "array" then
    .tool_response[0].is_error // false
  else
    false
  end
' 2>/dev/null)
if [[ "$MCP_ERROR" == "true" ]]; then
  exit 0
fi

# ── Extract result ────────────────────────────────────────────────────

# Single-pass unpack: handles string-encoded, array, or object responses.
RESULT=$(printf '%s' "$INPUT" | jq -r '
  def unpack: if type == "string" then (fromjson? // .) else . end;
  if (.tool_response | type) == "array" then
    (.tool_response[0].text // "" | unpack)
  else
    (.tool_response | unpack)
  end
  | if type == "object" and has("result") then (.result | unpack) else . end
' 2>/dev/null)

# Fallback chain: try text extraction, then raw tool_response.
if [[ -z "$RESULT" ]]; then
  RESULT=$(printf '%s' "$INPUT" | jq -r '.tool_response[0].text // empty' 2>/dev/null)
fi
if [[ -z "$RESULT" ]]; then
  RESULT=$(printf '%s' "$INPUT" | jq -r '.tool_response | if type == "string" then . else tostring end' 2>/dev/null)
fi
# If we couldn't extract any result or got literal "null", let Claude Code show the original.
[[ -z "$RESULT" || "$RESULT" == "null" ]] && exit 0

# Check for error field in extracted result
RESULT_ERROR=$(printf '%s' "$RESULT" | jq -r '.error // empty' 2>/dev/null)
if [[ -n "$RESULT_ERROR" ]]; then
  emit_simple "error: ${RESULT_ERROR}"
  exit 0
fi

# ── whoami ────────────────────────────────────────────────────────────
if [[ "$TOOL_NAME" == "whoami" ]]; then
  NAME=$(printf '%s' "$RESULT" | jq -r '.name // empty' 2>/dev/null)
  if [[ -n "$NAME" ]]; then
    SUMMARY=$(printf '%s' "$RESULT" | jq -r '
      [.name + " (" + .handle + ") — " + .kind] +
      (if .email != null and .email != "" then ["Email: " + .email] else [] end) +
      (if .github != null and .github != "" then ["GitHub: " + .github] else [] end) +
      (if .personality != null and .personality != "" then ["Personality: " + .personality] else [] end) +
      (if .writing_style != null and .writing_style != "" then ["Writing: " + .writing_style] else [] end) +
      (if (.skills // [] | length) > 0 then ["Skills: " + (.skills | join(", "))] else [] end)
      | join("\n")
    ' 2>/dev/null)
    emit "$SUMMARY" "$RESULT"
  else
    emit_simple "$RESULT"
  fi
  exit 0
fi

# ── list_identities ──────────────────────────────────────────────────
if [[ "$TOOL_NAME" == "list_identities" ]]; then
  NAMES=$(printf '%s' "$RESULT" | jq -r '[.[] | (if .active then "* " else "" end) + .handle + " (" + (.name // "?") + ")"] | join(", ")' 2>/dev/null)
  [[ -z "$NAMES" ]] && NAMES="(none)"
  emit "$NAMES" "$RESULT"
  exit 0
fi

# ── get_identity ─────────────────────────────────────────────────────
if [[ "$TOOL_NAME" == "get_identity" ]]; then
  SUMMARY=$(printf '%s' "$RESULT" | jq -r '
    [.name + " (" + .handle + ") — " + .kind] +
    (if .email != null and .email != "" then ["Email: " + .email] else [] end) +
    (if .github != null and .github != "" then ["GitHub: " + .github] else [] end) +
    (if .voice != null and .voice.provider != null and .voice.provider != "" then ["Voice: " + .voice.provider + "/" + (.voice.voice_id // "")] else [] end) +
    (if .personality != null and .personality != "" then ["Personality: " + .personality] else [] end) +
    (if .writing_style != null and .writing_style != "" then ["Writing: " + .writing_style] else [] end) +
    (if (.skills // [] | length) > 0 then ["Skills: " + (.skills | join(", "))] else [] end)
    | join("\n")
  ' 2>/dev/null)
  emit "$SUMMARY" "$RESULT"
  exit 0
fi

# ── create_identity ──────────────────────────────────────────────────
if [[ "$TOOL_NAME" == "create_identity" ]]; then
  NAME=$(printf '%s' "$RESULT" | jq -r '.name // empty' 2>/dev/null)
  [[ -z "$NAME" ]] && NAME="identity"
  emit "Created ${NAME}" "$RESULT"
  exit 0
fi

# ── list_skills / list_personalities / list_writing_styles ────────────
if [[ "$TOOL_NAME" == "list_skills" || "$TOOL_NAME" == "list_personalities" || "$TOOL_NAME" == "list_writing_styles" ]]; then
  SLUGS=$(printf '%s' "$RESULT" | jq -r '[(.attributes // [])[].slug] | join(", ")' 2>/dev/null)
  [[ -z "$SLUGS" ]] && SLUGS="(none)"
  emit "$SLUGS" "$RESULT"
  exit 0
fi

# ── get_skill / get_personality / get_writing_style ───────────────────
if [[ "$TOOL_NAME" == "get_skill" || "$TOOL_NAME" == "get_personality" || "$TOOL_NAME" == "get_writing_style" ]]; then
  CONTENT=$(printf '%s' "$RESULT" | jq -r '.content // empty' 2>/dev/null)
  emit "$CONTENT" "$RESULT"
  exit 0
fi

# ── create_skill / create_personality / create_writing_style ──────────
if [[ "$TOOL_NAME" == "create_skill" || "$TOOL_NAME" == "create_personality" || "$TOOL_NAME" == "create_writing_style" ]]; then
  SLUG=$(printf '%s' "$RESULT" | jq -r '.slug // empty' 2>/dev/null)
  [[ -z "$SLUG" ]] && SLUG="attribute"
  emit "Created ${SLUG}" "$RESULT"
  exit 0
fi

# ── set_personality / set_writing_style / add_skill / remove_skill ────
if [[ "$TOOL_NAME" == "set_personality" || "$TOOL_NAME" == "set_writing_style" || "$TOOL_NAME" == "add_skill" || "$TOOL_NAME" == "remove_skill" ]]; then
  emit_simple "$RESULT"
  exit 0
fi

# ── session tools ────────────────────────────────────────────────────
if [[ "$TOOL_NAME" == "session_roster" ]]; then
  emit "Roster loaded" "$RESULT"
  exit 0
fi

if [[ "$TOOL_NAME" == "session_iam" || "$TOOL_NAME" == "session_join" || "$TOOL_NAME" == "session_leave" ]]; then
  emit_simple "$RESULT"
  exit 0
fi

# ── ext tools ────────────────────────────────────────────────────────
if [[ "$TOOL_NAME" == "ext_get" || "$TOOL_NAME" == "ext_list" ]]; then
  emit "Extensions" "$RESULT"
  exit 0
fi

if [[ "$TOOL_NAME" == "ext_set" || "$TOOL_NAME" == "ext_del" ]]; then
  emit_simple "$RESULT"
  exit 0
fi

# ── Fallback ─────────────────────────────────────────────────────────
emit_simple "$RESULT"

exit 0
