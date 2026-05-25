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
HOOK_INPUT=""
IFS= read -r -t 1 HOOK_INPUT 2>/dev/null || true
printf '%s\n' "$HOOK_INPUT" | ethos hook pre-tool-use 2>>"$HOME/.punt-labs/ethos/hook-errors.log" || true
