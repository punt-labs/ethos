#!/usr/bin/env bash
# hooks/audit-log.sh — Append tool invocation to session audit log
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
HOOK_INPUT=""
IFS= read -r -t 1 HOOK_INPUT 2>/dev/null || true
printf '%s\n' "$HOOK_INPUT" | ethos hook audit-log 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
