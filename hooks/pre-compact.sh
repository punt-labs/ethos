#!/usr/bin/env bash
# hooks/pre-compact.sh — Thin gate for PreCompact hook
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
ethos hook pre-compact < /dev/stdin 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
