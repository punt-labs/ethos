#!/usr/bin/env bash
# hooks/suppress-output.sh — Thin gate for PostToolUse output formatting
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
ethos hook format-output < /dev/stdin 2>/dev/null || true
