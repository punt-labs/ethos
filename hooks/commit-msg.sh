#!/bin/sh
# hooks/commit-msg.sh — Append Mission:/Delegation: git trailers when env is set.
# DES-054 phase 3: connects git history to the audited delegation chain so
# `git log --grep Mission:` becomes a forensic search tool. Passthrough when
# neither env var is set — safe for every commit on every repo.
#
# Idempotency: re-running on a message already carrying the trailer leaves
# it unchanged. Uses git-interpret-trailers when available; falls back to
# a plain append with a blank-line separator.
[ -z "$1" ] && exit 0
msg_file="$1"
[ -f "$msg_file" ] || exit 0
# Fallback: when MISSION_ID/DELEGATION_ID aren't in env (the common
# case for subagent commits — additional_env doesn't persist into
# subprocess env), read the delegation-binding sidecar written by
# the PreToolUse Tier B dispatch. Pick the most recently modified
# binding across all sessions — correct for single-user single-
# session, the common case.
if [ -z "${MISSION_ID:-}" ] && [ -z "${DELEGATION_ID:-}" ]; then
  binding_file=$(find "$HOME/.punt-labs/ethos/sessions" -name delegation-binding -type f -print 2>/dev/null | head -1)
  if [ -n "$binding_file" ] && [ -f "$binding_file" ]; then
    DELEGATION_ID=$(sed -n '1p' "$binding_file")
    MISSION_ID=$(sed -n '2p' "$binding_file")
    export DELEGATION_ID MISSION_ID
  fi
fi
[ -z "${MISSION_ID:-}" ] && [ -z "${DELEGATION_ID:-}" ] && exit 0
add_trailer() {
  key=$1
  val=$2
  # Idempotency check: scan only the trailer block (everything
  # after the last blank line) so a commit message body that
  # quotes a previous "Mission: " line cannot trigger a false
  # positive (Bugbot LOW on PR #328). awk emits the paragraph
  # following the last blank line; if no blank line exists, the
  # whole message is one paragraph and we scan all of it.
  trailer_block=$(awk '
    /^[[:space:]]*$/ { block = ""; next }
    { block = block ? block ORS $0 : $0 }
    END { print block }
  ' "$msg_file")
  if printf '%s\n' "$trailer_block" | grep -q "^${key}: "; then
    return 0
  fi
  if command -v git >/dev/null 2>&1; then
    # mktemp failure (no write perm on .git dir, /tmp full, etc.)
    # falls through to the plain-append path rather than dropping
    # the trailer — the trailer must land even when the git path
    # is unavailable (Bugbot LOW on PR #328: previously
    # `|| return 1` exited early with no fallback).
    tmp=$(mktemp "${msg_file}.XXXXXX" 2>/dev/null)
    if [ -z "$tmp" ]; then
      printf 'ethos: commit-msg: mktemp failed; using plain append\n' >&2
    elif git interpret-trailers --trailer "${key}: ${val}" "$msg_file" > "$tmp"; then
      # mv can fail on permissions, cross-filesystem, or
      # disk-full. If it does, the temp file is stale and the
      # commit message is untouched — fall through to the plain
      # append path so the trailer still lands (Copilot on PR
      # #328: previously returned 0 after a silent mv failure).
      if mv "$tmp" "$msg_file"; then
        return 0
      fi
      rm -f "$tmp"
      printf 'ethos: commit-msg: mv onto %s failed; using plain append\n' "$msg_file" >&2
    else
      rm -f "$tmp"
      printf 'ethos: commit-msg: git interpret-trailers failed; using plain append\n' >&2
    fi
  fi
  # Plain-append fallback. Ensure the file ends with a blank line
  # separating the body from the trailer block, then append the
  # trailer without a leading newline. Multiple back-to-back
  # add_trailer calls then form one contiguous trailer block —
  # putting `\n` before each entry would interleave blank lines
  # and break git's trailer parser (Bugbot MED on PR #328).
  if [ -s "$msg_file" ]; then
    last_char=$(tail -c1 "$msg_file" 2>/dev/null || true)
    if [ "$last_char" != "" ] && [ "$last_char" != "$(printf '\n')" ]; then
      printf '\n' >> "$msg_file"
    fi
    last_line=$(tail -n1 "$msg_file" 2>/dev/null || true)
    # An empty last line means there's already a paragraph break.
    # A trailer-shaped last line (Key: Value) means the previous
    # add_trailer call planted one — continue the block, no extra
    # blank. Anything else is body text — insert a blank line so
    # git's trailer parser sees a separate paragraph.
    if [ -z "$last_line" ]; then
      :
    elif printf '%s\n' "$last_line" | grep -Eq '^[A-Za-z][A-Za-z0-9-]*: '; then
      :
    else
      printf '\n' >> "$msg_file"
    fi
  fi
  printf '%s: %s\n' "$key" "$val" >> "$msg_file"
}
[ -n "${MISSION_ID:-}" ] && add_trailer Mission "$MISSION_ID"
[ -n "${DELEGATION_ID:-}" ] && add_trailer Delegation "$DELEGATION_ID"
exit 0
