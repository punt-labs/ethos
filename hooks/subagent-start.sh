#!/usr/bin/env bash
# hooks/subagent-start.sh — SubagentStart hook for ethos plugin
# Registers a subagent in the session roster.
set -euo pipefail

command -v ethos >/dev/null 2>&1 || exit 0

ETHOS_LOG="$HOME/.punt-labs/ethos/hook-errors.log"
mkdir -p "$(dirname "$ETHOS_LOG")"

# Read hook input from stdin.
INPUT=$(cat)
AGENT_ID=$(echo "$INPUT" | grep -o '"agent_id" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)
AGENT_TYPE=$(echo "$INPUT" | grep -o '"agent_type" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)
SESSION_ID=$(echo "$INPUT" | grep -o '"session_id" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)

[[ -n "$AGENT_ID" ]] || exit 0
[[ -n "$SESSION_ID" ]] || exit 0

# Resolve persona from agent_type — convention: identity with same name.
PERSONA=""
if [[ -n "$AGENT_TYPE" ]]; then
  if ethos show "$AGENT_TYPE" >/dev/null 2>>"$ETHOS_LOG"; then
    PERSONA="$AGENT_TYPE"
  fi
fi

# Parent is the primary agent (Claude PID). Use PPID as fallback.
PARENT="${PPID}"

ethos session join \
  --agent-id "$AGENT_ID" \
  --persona "$PERSONA" \
  --parent "$PARENT" \
  --agent-type "$AGENT_TYPE" \
  --session "$SESSION_ID" 2>>"$ETHOS_LOG" || true
