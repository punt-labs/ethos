#!/usr/bin/env bash
# hooks/pre-tool-use.sh — PreToolUse hook dispatcher for ethos.
#
# Wires Claude Code's PreToolUse event to `ethos hook pre-tool-use`,
# which implements the DES-054 Tier A / Tier B dispatch for `Agent`
# tool calls (allocates DELEGATION_ID, optionally writes the delegation
# skeleton, emits the additional_env block the spawned worker
# inherits) and the verifier file-allowlist enforcement for Write/Edit.
#
# Bypasses:
#   - $HOME/.punt-hooks-kill — operator kill switch for all punt hooks.
#   - missing $HOME/.punt-labs/ethos — ethos not configured on this
#     machine; nothing to do.
#   - missing `ethos` binary on PATH — same.
#
# Errors from the handler go to a per-machine log so a broken hook
# never blocks the tool call (`|| true` on the pipeline).
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
  { printf 'ethos pre-tool-use: stdin read timed out after 10s (%s bytes)\n' "$(printf %s "$HOOK_INPUT" | wc -c | tr -d ' ')" >>"$HOME/.punt-labs/ethos/hook-errors.log"; } 2>/dev/null
fi
printf '%s\n' "$HOOK_INPUT" | ethos hook pre-tool-use 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
