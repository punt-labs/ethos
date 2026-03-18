#!/usr/bin/env bash
# hooks/session-end.sh — SessionEnd hook for ethos plugin
# Tears down the session roster and cleans up the current PID file.
set -euo pipefail

command -v ethos >/dev/null 2>&1 || exit 0

ETHOS_LOG="$HOME/.punt-labs/ethos/hook-errors.log"
mkdir -p "$(dirname "$ETHOS_LOG")"

# Read hook input from stdin.
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | grep -o '"session_id" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)

[[ -n "$SESSION_ID" ]] || exit 0

# Delete the roster through the CLI (respects locking).
ethos session delete --session "$SESSION_ID" 2>>"$ETHOS_LOG" || true

# Clean up PID-keyed current session file.
CLAUDE_PID="${PPID}"
ethos session delete-current --pid "$CLAUDE_PID" 2>>"$ETHOS_LOG" || true
