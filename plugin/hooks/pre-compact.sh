#!/usr/bin/env bash
# hooks/pre-compact.sh — Thin gate for PreCompact hook
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
# Claude Code's fd 0 is inherited through /bin/sh and Go cannot read it
# reliably on Linux (DES-029): bash reads the whole payload, ethos gets a fresh pipe.
HOOK_INPUT=""
_rc=0
_hook=pre-compact
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
    # A hook cancelled mid-read must not leave the payload (it can hold
    # secrets) on disk or cat running.
    trap 'rm -f "$_f"; kill "$_p" 2>/dev/null' EXIT
    trap 'exit 143' INT TERM
    cat <&0 >"$_f" &
    _p=$!
    _end=$((SECONDS + 10))
    while kill -0 "$_p" 2>/dev/null; do
      if [[ $SECONDS -ge $_end ]]; then _rc=142; kill "$_p" 2>/dev/null; break; fi
      sleep 0.05
    done
    wait "$_p" 2>/dev/null
    _w=$?
    if [[ $_rc -eq 0 && $_w -ne 0 ]]; then
      { printf 'ethos %s: stdin capture failed (cat exit %s)\n' "$_hook" "$_w" >>"$HOME/.punt-labs/ethos/hook-errors.log"; } 2>/dev/null
    fi
    HOOK_INPUT=$(cat "$_f"; printf x)
    HOOK_INPUT=${HOOK_INPUT%x}
    rm -f "$_f"
    trap - EXIT INT TERM
  else
    { printf 'ethos %s: mktemp failed in %s and /tmp; using bash 3.2 read, a partial payload will be lost\n' "$_hook" "${TMPDIR:-/tmp}" >>"$HOME/.punt-labs/ethos/hook-errors.log"; } 2>/dev/null
    IFS= read -r -d "" -t 10 HOOK_INPUT 2>/dev/null || _rc=$?
  fi
fi
# read returns >128 when the 10s deadline fired: log it, then forward what arrived.
if [[ $_rc -gt 128 ]]; then
  { printf 'ethos pre-compact: stdin read timed out after 10s (%s bytes)\n' "$(printf %s "$HOOK_INPUT" | wc -c | tr -d ' ')" >>"$HOME/.punt-labs/ethos/hook-errors.log"; } 2>/dev/null
fi
printf '%s\n' "$HOOK_INPUT" | ethos hook pre-compact 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
