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
[ -z "${MISSION_ID:-}" ] && [ -z "${DELEGATION_ID:-}" ] && exit 0
add_trailer() {
  key=$1
  val=$2
  grep -q "^${key}: " "$msg_file" && return 0
  if command -v git >/dev/null 2>&1; then
    tmp=$(mktemp "${msg_file}.XXXXXX") || return 1
    git interpret-trailers --trailer "${key}: ${val}" "$msg_file" > "$tmp" && mv "$tmp" "$msg_file"
    return 0
  fi
  printf '\n%s: %s\n' "$key" "$val" >> "$msg_file"
}
[ -n "${MISSION_ID:-}" ] && add_trailer Mission "$MISSION_ID"
[ -n "${DELEGATION_ID:-}" ] && add_trailer Delegation "$DELEGATION_ID"
exit 0
