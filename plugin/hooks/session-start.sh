#!/usr/bin/env bash
# hooks/session-start.sh — Thin gate for SessionStart hook
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
# Claude Code's fd 0 is inherited through /bin/sh and Go cannot read it
# reliably on Linux (DES-029): bash reads the whole payload, ethos gets a fresh pipe.
HOOK_INPUT=""
_rc=0
IFS= read -r -d "" -t 10 HOOK_INPUT 2>/dev/null || _rc=$?
# read returns >128 when the 10s deadline fired: log it, then forward what arrived.
if [[ $_rc -gt 128 ]]; then
  { printf 'ethos session-start: stdin read timed out after 10s (%s bytes)\n' "$(printf %s "$HOOK_INPUT" | wc -c | tr -d ' ')" >>"$HOME/.punt-labs/ethos/hook-errors.log"; } 2>/dev/null
fi
printf '%s\n' "$HOOK_INPUT" | ethos hook session-start 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
