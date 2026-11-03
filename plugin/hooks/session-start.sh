#!/usr/bin/env bash
# hooks/session-start.sh — Thin gate for SessionStart hook
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
# stdin goes to ethos unread: ethos bounds the wait (internal/hook readWithTimeout,
# which handles the inherited fd 0 of DES-029), and a bash read -t 1 here
# truncated payloads over 8 KiB.
ethos hook session-start 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
