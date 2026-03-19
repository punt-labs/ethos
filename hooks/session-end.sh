#!/usr/bin/env bash
# hooks/session-end.sh — SessionEnd hook for ethos plugin
# Tears down the session roster and cleans up the current PID file.
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

SESSION_ID=$(echo "$INPUT" | grep -o '"session_id" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)

[[ -n "$SESSION_ID" ]] || exit 0

# Delete the roster through the CLI (respects locking).
ethos session delete --session "$SESSION_ID" 2>>"$ETHOS_LOG" || true

# Clean up PID-keyed current session file.
CLAUDE_PID="${PPID}"
ethos session delete-current --pid "$CLAUDE_PID" 2>>"$ETHOS_LOG" || true

exit 0
