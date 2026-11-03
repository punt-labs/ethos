#!/usr/bin/env bash
# hooks/audit-log.sh — Append tool invocation to session audit log
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
# stdin goes to ethos unread: ethos bounds the wait, and a bash read -t 1 here
# truncated payloads over 8 KiB.
ethos hook audit-log 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
