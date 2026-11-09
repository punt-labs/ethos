#!/usr/bin/env bash
# hooks/session-end.sh — Thin gate for SessionEnd hook
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
# Claude Code's fd 0 is inherited through /bin/sh and Go cannot read it
# reliably on Linux (DES-029): bash reads the whole payload, ethos gets a fresh pipe.
HOOK_INPUT=""
_rc=0
_hook=session-end
# This block is identical in every stdin wrapper in this directory
# (TestWrappersShareOneStdinReadBlock enforces it); change all of them together.
if [[ ${BASH_VERSINFO[0]} -ge 4 ]]; then
  IFS= read -r -d "" -t 10 HOOK_INPUT 2>/dev/null || _rc=$?
else
  # bash 3.2 (macOS /bin/bash) discards a partial read when -t fires, so the
  # fragment would be lost: let cat collect stdin into a file under the same
  # 10s deadline and read the file back. The file is mode 0600 and lives at
  # most 10s; it follows TMPDIR, with /tmp as the fallback.
  _f=$(mktemp "${TMPDIR:-/tmp}/ethos-hook.XXXXXX" 2>/dev/null) ||
    _f=$(mktemp /tmp/ethos-hook.XXXXXX 2>/dev/null) || _f=""
  if [[ -n $_f ]]; then
    _p=""
    _d=""
    _r=""
    # A hook cancelled mid-read must not leave the payload (it can hold
    # secrets) on disk, or cat or the watchdog running.
    trap 'rm -f "$_f" "$_r"; kill "$_p" "$_d" 2>/dev/null' EXIT
    trap 'exit 143' INT TERM
    cat <&0 >"$_f" &
    _p=$!
    # The deadline is a watchdog shell around sleep, not a poll: it costs one
    # fork when stdin closes promptly. It traps TERM and forwards it to its
    # sleep, and says so through a fifo before we may kill it, so no kill can
    # land before the trap exists and orphan the sleep.
    _r=$(mktemp -u "${TMPDIR:-/tmp}/ethos-hook-rdy.XXXXXX") && mkfifo "$_r" || _r=""
    ( trap 'kill "$_s" 2>/dev/null; exit 0' TERM; sleep 10 & _s=$!; [[ -n $_r ]] && echo ready >"$_r"; wait "$_s" && kill "$_p" 2>/dev/null ) >/dev/null 2>&1 &
    _d=$!
    # Without the fifo there is no handshake: give the watchdog a moment to arm.
    if [[ -n $_r ]]; then read -r _ <"$_r"; else sleep 0.1; fi
    rm -f "$_r"
    wait "$_p" 2>/dev/null
    _st=$?
    kill "$_d" 2>/dev/null
    wait "$_d" 2>/dev/null
    _d=""
    if [[ $_st -gt 128 ]]; then
      _rc=142
    elif [[ $_st -ne 0 ]]; then
      { printf 'ethos %s: stdin capture failed (cat exit %s)\n' "$_hook" "$_st" >>"$HOME/.punt-labs/ethos/hook-errors.log"; } 2>/dev/null
    fi
    IFS= read -r -d "" HOOK_INPUT <"$_f" 2>/dev/null
    rm -f "$_f"
    trap - EXIT INT TERM
  else
    { printf 'ethos %s: mktemp failed in %s and /tmp; using bash 3.2 read, a partial payload will be lost\n' "$_hook" "${TMPDIR:-/tmp}" >>"$HOME/.punt-labs/ethos/hook-errors.log"; } 2>/dev/null
    IFS= read -r -d "" -t 10 HOOK_INPUT 2>/dev/null || _rc=$?
  fi
fi
# read returns >128 when the 10s deadline fired: log it, then forward what arrived.
if [[ $_rc -gt 128 ]]; then
  { printf 'ethos session-end: stdin read timed out after 10s (%s bytes)\n' "$(printf %s "$HOOK_INPUT" | wc -c | tr -d ' ')" >>"$HOME/.punt-labs/ethos/hook-errors.log"; } 2>/dev/null
fi
printf '%s\n' "$HOOK_INPUT" | ethos hook session-end 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
