#!/usr/bin/env bash
# hooks/session-start.sh — SessionStart hook for ethos plugin
set -euo pipefail

PLUGIN_ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
SETTINGS="$HOME/.claude/settings.json"
COMMANDS_DIR="$HOME/.claude/commands"
ETHOS_LOG="$HOME/.punt-labs/ethos/hook-errors.log"
TOOL_PATTERN="mcp__plugin_ethos_self__"
DEV_TOOL_PATTERN="mcp__plugin_ethos-dev_self__"
mkdir -p "$(dirname "$ETHOS_LOG")"

# Read session_id from stdin JSON (Claude Code passes hook context).
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | grep -o '"session_id" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)

# ── Detect dev mode ──────────────────────────────────────────────────
IS_DEV=false
if command -v jq &>/dev/null && [[ -f "$PLUGIN_ROOT/.claude-plugin/plugin.json" ]]; then
  plugin_name="$(jq -r '.name // ""' "$PLUGIN_ROOT/.claude-plugin/plugin.json")"
  if [[ "$plugin_name" == *-dev ]]; then
    IS_DEV=true
  fi
fi

# ── Deploy top-level commands (diff-and-copy, not skip-if-exists) ────
# Skip entirely in dev mode — prod plugin deploys top-level commands
DEPLOYED=()
if [[ "$IS_DEV" == "false" ]]; then
  for cmd_file in "$PLUGIN_ROOT/commands/"*.md; do
    [[ -f "$cmd_file" ]] || continue
    name="$(basename "$cmd_file")"
    [[ "$name" == *-dev.md ]] && continue
    dest="$COMMANDS_DIR/$name"
    mkdir -p "$COMMANDS_DIR"
    if [[ ! -f "$dest" ]] || ! diff -q "$cmd_file" "$dest" >/dev/null 2>&1; then
      cp "$cmd_file" "$dest"
      DEPLOYED+=("/${name%.md}")
    fi
  done
fi

# ── Allow MCP tools in user settings if not already allowed ──────────
if ! command -v jq &>/dev/null; then
  echo "[$(date -Iseconds)] WARN: jq not found — skipping MCP tool auto-allow" >> "$ETHOS_LOG"
elif [[ -f "$SETTINGS" ]]; then
  PERMS_CHANGED=false

  # Allow prod tools
  if ! jq -e ".permissions.allow // [] | map(select(contains(\"$TOOL_PATTERN\"))) | length > 0" "$SETTINGS" >/dev/null 2>&1; then
    TMPFILE="$(mktemp "${SETTINGS}.tmp.XXXXXX")"
    if jq '.permissions.allow = (.permissions.allow // []) + ["mcp__plugin_ethos_self__*"]' "$SETTINGS" > "$TMPFILE"; then
      mv "$TMPFILE" "$SETTINGS"
      PERMS_CHANGED=true
    else
      echo "[$(date -Iseconds)] ERROR: jq failed updating settings.json" >> "$ETHOS_LOG"
      rm -f "$TMPFILE"
    fi
  fi

  # Allow dev tools (only when running as ethos-dev)
  if [[ "$IS_DEV" == "true" ]]; then
    if ! jq -e ".permissions.allow // [] | map(select(contains(\"$DEV_TOOL_PATTERN\"))) | length > 0" "$SETTINGS" >/dev/null 2>&1; then
      TMPFILE="$(mktemp "${SETTINGS}.tmp.XXXXXX")"
      if jq '.permissions.allow = (.permissions.allow // []) + ["mcp__plugin_ethos-dev_self__*"]' "$SETTINGS" > "$TMPFILE"; then
        mv "$TMPFILE" "$SETTINGS"
        PERMS_CHANGED=true
      else
        echo "[$(date -Iseconds)] ERROR: jq failed updating settings.json (dev)" >> "$ETHOS_LOG"
        rm -f "$TMPFILE"
      fi
    fi
  fi

  if [[ "$PERMS_CHANGED" == "true" ]]; then
    DEPLOYED+=("auto-allowed ethos MCP tools")
  fi
fi

# Resolve active identity for context injection
IDENTITY_INFO=""
ACTIVE_PERSONA=""
if command -v ethos >/dev/null 2>&1; then
  IDENTITY_INFO=$(ethos whoami 2>>"$ETHOS_LOG" || true)
  ACTIVE_PERSONA=$(ethos whoami --json 2>>"$ETHOS_LOG" | grep -o '"handle" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)
fi

# Create session roster if we have a session ID and ethos is available
if [[ -n "$SESSION_ID" ]] && command -v ethos >/dev/null 2>&1; then
  USER_ID="${USER:-$(whoami)}"
  USER_PERSONA="${ACTIVE_PERSONA:-$USER_ID}"

  # Parent agent is PPID (Claude Code process)
  CLAUDE_PID="${PPID}"

  # Create roster with root (human) and primary (claude agent).
  # Only write the current-session PID file if create succeeds.
  if ethos session create \
    --session "$SESSION_ID" \
    --root-id "$USER_ID" \
    --root-persona "$USER_PERSONA" \
    --primary-id "$CLAUDE_PID" \
    --primary-persona "${ACTIVE_PERSONA:-agent}" 2>>"$ETHOS_LOG"; then
    ethos session write-current --pid "$CLAUDE_PID" --session "$SESSION_ID" 2>>"$ETHOS_LOG" || true
  fi
fi

# Build output
OUTPUT=""
if [[ ${#DEPLOYED[@]} -gt 0 ]]; then
  OUTPUT="Ethos: deployed commands: ${DEPLOYED[*]}. "
fi
if [[ -n "$IDENTITY_INFO" ]]; then
  OUTPUT="${OUTPUT}Active identity: ${IDENTITY_INFO}"
fi
if [[ -n "$SESSION_ID" ]]; then
  [[ -n "$OUTPUT" ]] && OUTPUT="${OUTPUT} "
  OUTPUT="${OUTPUT}Session: ${SESSION_ID}"
fi

if [[ -n "$OUTPUT" ]]; then
  # Escape characters that would break JSON string values.
  ESCAPED=$(printf '%s' "$OUTPUT" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/\\t/g' | tr '\n' ' ' | tr '\r' ' ')
  printf '{"hookSpecificOutput":{"additionalContext":"%s"}}' "$ESCAPED"
fi
