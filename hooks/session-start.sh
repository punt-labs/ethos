#!/usr/bin/env bash
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
set -euo pipefail

PLUGIN_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SETTINGS="$HOME/.claude/settings.json"
COMMANDS_DIR="$HOME/.claude/commands"
ETHOS_LOG="$HOME/.punt-labs/ethos/hook-errors.log"
mkdir -p "$(dirname "$ETHOS_LOG")"

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
    TMPFILE="$(mktemp)"
    jq --arg g "$PROD_GLOB" '.permissions.allow = (.permissions.allow // []) + [$g]' "$SETTINGS" > "$TMPFILE"
    mv "$TMPFILE" "$SETTINGS"
    CHANGED=true
  fi

  # Allow dev tools (only when running as ethos-dev)
  if [[ "$IS_DEV" == "true" ]]; then
    if ! jq -e ".permissions.allow // [] | index(\"$DEV_GLOB\")" "$SETTINGS" >/dev/null 2>&1; then
      TMPFILE="$(mktemp)"
      jq --arg g "$DEV_GLOB" '.permissions.allow = (.permissions.allow // []) + [$g]' "$SETTINGS" > "$TMPFILE"
      mv "$TMPFILE" "$SETTINGS"
      CHANGED=true
    fi
  fi

  if [[ "$CHANGED" == "true" ]]; then
    ACTIONS+=("Auto-allowed ethos MCP tools in permissions")
  fi
fi

# ── Resolve active identity ──────────────────────────────────────────
IDENTITY_INFO=""
if command -v ethos >/dev/null 2>&1; then
  IDENTITY_INFO=$(ethos whoami 2>>"$ETHOS_LOG" || true)
fi

if [[ -n "$IDENTITY_INFO" ]]; then
  ACTIONS+=("Active identity: ${IDENTITY_INFO}")
fi

# ── Notify Claude if anything was set up ─────────────────────────────
if [[ ${#ACTIONS[@]} -gt 0 ]]; then
  MSG="Ethos plugin setup complete."
  for action in "${ACTIONS[@]}"; do
    MSG="$MSG $action."
  done
  cat <<ENDJSON
{
  "hookSpecificOutput": {
    "hookEventName": "SessionStart",
    "additionalContext": "$MSG"
  }
}
ENDJSON
fi

exit 0
