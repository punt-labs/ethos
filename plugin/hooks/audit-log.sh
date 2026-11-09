#!/usr/bin/env bash
# hooks/audit-log.sh — Append tool invocation to session audit log
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
# Claude Code's fd 0 is inherited through /bin/sh and Go cannot read it
# reliably on Linux (DES-029): bash reads the whole payload, ethos gets a fresh pipe.
HOOK_INPUT=""
_rc=0
if [[ ${BASH_VERSINFO[0]} -ge 4 ]]; then
  IFS= read -r -d "" -t 10 HOOK_INPUT 2>/dev/null || _rc=$?
else
  # bash 3.2 (macOS /bin/bash) discards a partial read when -t fires, so the
  # fragment would be lost: let cat collect stdin into a file under the same
  # 10s deadline and read the file back.
  _f=$(mktemp "${TMPDIR:-/tmp}/ethos-hook.XXXXXX" 2>/dev/null) || _f=""
  if [[ -n $_f ]]; then
    cat <&0 >"$_f" &
    _p=$!
    _end=$((SECONDS + 10))
    while kill -0 "$_p" 2>/dev/null; do
      if [[ $SECONDS -ge $_end ]]; then _rc=142; kill "$_p" 2>/dev/null; break; fi
      sleep 0.05
    done
    wait "$_p" 2>/dev/null
    HOOK_INPUT=$(cat "$_f"; printf x)
    HOOK_INPUT=${HOOK_INPUT%x}
    rm -f "$_f"
  else
    IFS= read -r -d "" -t 10 HOOK_INPUT 2>/dev/null || _rc=$?
  fi
fi
# read returns >128 when the 10s deadline fired: log it, then forward what arrived.
if [[ $_rc -gt 128 ]]; then
  { printf 'ethos audit-log: stdin read timed out after 10s (%s bytes)\n' "$(printf %s "$HOOK_INPUT" | wc -c | tr -d ' ')" >>"$HOME/.punt-labs/ethos/hook-errors.log"; } 2>/dev/null
fi
printf '%s\n' "$HOOK_INPUT" | ethos hook audit-log 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
