#!/usr/bin/env bash
# hooks/session-end.sh — Thin gate for SessionEnd hook
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
ethos hook session-end 2>/dev/null || true
