#!/usr/bin/env bash
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
set -euo pipefail

PLUGIN_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SETTINGS="$HOME/.claude/settings.json"
COMMANDS_DIR="$HOME/.claude/commands"
ETHOS_LOG="$HOME/.punt-labs/ethos/hook-errors.log"
mkdir -p "$(dirname "$ETHOS_LOG")"

# Non-blocking stdin read for session_id (Claude Code may not close pipe)
SESSION_ID=""
if read -r -t 1 INPUT_LINE; then
  SESSION_ID=$(echo "$INPUT_LINE" | grep -o '"session_id" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)
fi

ACTIONS=()

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
if [[ "$IS_DEV" == "false" ]]; then
  mkdir -p "$COMMANDS_DIR"
  DEPLOYED=()
  for cmd_file in "$PLUGIN_ROOT/commands/"*.md; do
    [[ -f "$cmd_file" ]] || continue
    name="$(basename "$cmd_file")"
    [[ "$name" == *-dev.md ]] && continue
    dest="$COMMANDS_DIR/$name"
    if [[ ! -f "$dest" ]] || ! diff -q "$cmd_file" "$dest" >/dev/null 2>&1; then
      cp "$cmd_file" "$dest"
      DEPLOYED+=("/${name%.md}")
    fi
  done
  if [[ ${#DEPLOYED[@]} -gt 0 ]]; then
    ACTIONS+=("Deployed commands: ${DEPLOYED[*]}")
  fi
fi

# ── Allow MCP tools in user settings if not already allowed ──────────
if command -v jq &>/dev/null && [[ -f "$SETTINGS" ]]; then
  CHANGED=false

  PROD_GLOB="mcp__plugin_ethos_self__*"
  DEV_GLOB="mcp__plugin_ethos-dev_self__*"

  # Allow prod tools
  if ! jq -e ".permissions.allow // [] | index(\"$PROD_GLOB\")" "$SETTINGS" >/dev/null 2>&1; then
    TMPFILE="$(mktemp "${SETTINGS}.tmp.XXXXXX")"
    if jq --arg g "$PROD_GLOB" '.permissions.allow = (.permissions.allow // []) + [$g]' "$SETTINGS" > "$TMPFILE"; then
      mv "$TMPFILE" "$SETTINGS"
      CHANGED=true
    else
      rm -f "$TMPFILE"
    fi
  fi

  # Allow dev tools (only when running as ethos-dev)
  if [[ "$IS_DEV" == "true" ]]; then
    if ! jq -e ".permissions.allow // [] | index(\"$DEV_GLOB\")" "$SETTINGS" >/dev/null 2>&1; then
      TMPFILE="$(mktemp "${SETTINGS}.tmp.XXXXXX")"
      if jq --arg g "$DEV_GLOB" '.permissions.allow = (.permissions.allow // []) + [$g]' "$SETTINGS" > "$TMPFILE"; then
        mv "$TMPFILE" "$SETTINGS"
        CHANGED=true
      else
        rm -f "$TMPFILE"
      fi
    fi
  fi

  if [[ "$CHANGED" == "true" ]]; then
    ACTIONS+=("Auto-allowed ethos MCP tools in permissions")
  fi
fi

# ── Resolve human and agent identities ───────────────────────────────
HUMAN_INFO=""
HUMAN_PERSONA=""
AGENT_PERSONA=""
if command -v ethos >/dev/null 2>&1; then
  WHOAMI_JSON=$(ethos whoami --json 2>>"$ETHOS_LOG" || true)
  if [[ -n "$WHOAMI_JSON" ]]; then
    HUMAN_PERSONA=$(echo "$WHOAMI_JSON" | grep -o '"handle" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)
    HUMAN_NAME=$(echo "$WHOAMI_JSON" | grep -o '"name" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)
    HUMAN_INFO="${HUMAN_NAME} (${HUMAN_PERSONA})"
  fi
  AGENT_PERSONA=$(ethos resolve-agent 2>>"$ETHOS_LOG" || true)
fi

if [[ -n "$HUMAN_INFO" ]]; then
  ACTIONS+=("Active identity: ${HUMAN_INFO}")
fi

# ── Create session roster ────────────────────────────────────────────
if [[ -n "$SESSION_ID" ]] && command -v ethos >/dev/null 2>&1; then
  USER_ID="${USER:-$(whoami)}"
  USER_PERSONA="${HUMAN_PERSONA:-$USER_ID}"
  CLAUDE_PID="${PPID}"

  if ethos session create \
    --session "$SESSION_ID" \
    --root-id "$USER_ID" \
    --root-persona "$USER_PERSONA" \
    --primary-id "$CLAUDE_PID" \
    --primary-persona "$AGENT_PERSONA" 2>>"$ETHOS_LOG"; then
    ethos session write-current --pid "$CLAUDE_PID" --session "$SESSION_ID" 2>>"$ETHOS_LOG" || true
  fi
fi

# ── Notify Claude if anything was set up ─────────────────────────────
if [[ ${#ACTIONS[@]} -gt 0 ]]; then
  MSG="Ethos plugin setup complete."
  for action in "${ACTIONS[@]}"; do
    MSG="$MSG $action."
  done
  jq -n --arg msg "$MSG" '{
    hookSpecificOutput: {
      hookEventName: "SessionStart",
      additionalContext: $msg
    }
  }'
fi

exit 0
