#!/usr/bin/env bash
# hooks/session-start.sh — Thin gate for SessionStart hook
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
# Claude Code spawns hooks via /bin/sh -c, so the Go binary inherits
# fd 0 from an intermediate shell — not a direct pipe. Go cannot
# reliably read that inherited fd on Linux (DES-029). Read in bash
# and forward over a fresh pipe.
# Note: read -r reads one line. Claude Code sends single-line JSON
# (hooks.ts: stdin.write(payload + '\n')). Multi-line would truncate.
HOOK_INPUT=""
IFS= read -r -t 1 HOOK_INPUT 2>/dev/null || true
printf '%s\n' "$HOOK_INPUT" | ethos hook session-start 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
