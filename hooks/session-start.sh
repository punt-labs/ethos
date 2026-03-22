#!/usr/bin/env bash
# hooks/session-start.sh — Thin gate for SessionStart hook
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
ethos hook session-start 2>/dev/null || true
