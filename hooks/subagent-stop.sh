#!/usr/bin/env bash
# hooks/subagent-stop.sh — SubagentStop hook for ethos plugin
# Removes a subagent from the session roster.
set -euo pipefail

command -v ethos >/dev/null 2>&1 || exit 0

ETHOS_LOG="$HOME/.punt-labs/ethos/hook-errors.log"

# Read hook input from stdin.
INPUT=$(cat)
AGENT_ID=$(echo "$INPUT" | grep -o '"agent_id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)
SESSION_ID=$(echo "$INPUT" | grep -o '"session_id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)

[[ -n "$AGENT_ID" ]] || exit 0
[[ -n "$SESSION_ID" ]] || exit 0

ethos session leave \
  --agent-id "$AGENT_ID" \
  --session "$SESSION_ID" 2>>"$ETHOS_LOG" || true
