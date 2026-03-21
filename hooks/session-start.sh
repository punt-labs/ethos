#!/usr/bin/env bash
# Session-start hook for ethos plugin.
#
# IMPORTANT: This hook must NOT write to ~/.claude/ (DES-013).
# Command deployment and permission setup belong in `ethos install`.
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
set -euo pipefail

ETHOS_LOG="$HOME/.punt-labs/ethos/hook-errors.log"
mkdir -p "$(dirname "$ETHOS_LOG")"

# Non-blocking stdin read for session_id (Claude Code may not close pipe)
SESSION_ID=""
if read -r -t 1 INPUT_LINE; then
  SESSION_ID=$(echo "$INPUT_LINE" | grep -o '"session_id" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)
fi

ACTIONS=()

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
  MSG="Ethos session started."
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
