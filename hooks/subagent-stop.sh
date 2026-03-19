#!/usr/bin/env bash
# hooks/subagent-stop.sh — SubagentStop hook for ethos plugin
# Removes a subagent from the session roster.
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
set -euo pipefail

command -v ethos >/dev/null 2>&1 || exit 0

ETHOS_LOG="$HOME/.punt-labs/ethos/hook-errors.log"
mkdir -p "$(dirname "$ETHOS_LOG")"

# Non-blocking stdin read (Claude Code may not close pipe promptly)
INPUT=""
if read -r -t 1 INPUT_LINE; then
  INPUT="$INPUT_LINE"
fi

AGENT_ID=$(echo "$INPUT" | grep -o '"agent_id" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)
SESSION_ID=$(echo "$INPUT" | grep -o '"session_id" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)

[[ -n "$AGENT_ID" ]] || exit 0
[[ -n "$SESSION_ID" ]] || exit 0

ethos session leave \
  --agent-id "$AGENT_ID" \
  --session "$SESSION_ID" 2>>"$ETHOS_LOG" || true

exit 0
