#!/bin/sh
# hooks/commit-msg.sh — Append Mission:/Delegation: git trailers when env is set.
# DES-054 phase 3: connects git history to the audited delegation chain so
# `git log --grep Mission:` becomes a forensic search tool. Passthrough when
# neither env var is set — safe for every commit on every repo.
#
# Idempotency: re-running on a message already carrying the trailer leaves
# it unchanged. Uses git-interpret-trailers when available; falls back to
# a plain append with a blank-line separator.
#
# Preserve a chained host hook's fall-through status: when install.sh appends
# this script after a foreign commit-msg hook, $? here is that hook's last
# command status; every passthrough returns it so chaining never masks a host
# hook that signals failure by fall-through. Standalone, $? = 0 as before.
_host_status=$?

# §2.7 marker gate: ethos does no commit-time work unless it is enabled in
# this repo. REPO_ROOT is resolved inside the hook (worktree-safe), not baked
# in at install time. Absent marker → exit with the captured host status,
# never a bare exit 0, so a chained host that signals failure by fall-through
# still blocks the commit even when ethos is dormant.
REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || exit "$_host_status"
[ -f "$REPO_ROOT/.punt-labs/ethos/enabled" ] || exit "$_host_status"

[ -z "$1" ] && exit "$_host_status"
msg_file="$1"
[ -f "$msg_file" ] || exit "$_host_status"
# Fallback: when MISSION_ID/DELEGATION_ID aren't in env (the common
# case for subagent commits — additional_env doesn't persist into
# subprocess env), ask ethos for the trailer values of the session that
# is COMMITTING. It resolves that session the way the rest of the CLI
# does — ETHOS_SESSION, else the Claude process-tree walk — and reads
# only that session's active-mission and delegation-binding sidecars.
#
# The hook used to pick the session itself: glob every session
# directory, reverse-sort by name, take the newest one holding an
# active-mission sidecar. With two concurrent sessions each holding a
# mission, a commit from the older session was stamped with the newer
# session's mission and delegation (ethos-pobi). Nothing about the
# committing process entered that decision.
#
# No resolvable session, no ethos on PATH, or no active mission means
# no trailer. The hook never guesses.
if [ -z "${MISSION_ID:-}" ] && [ -z "${DELEGATION_ID:-}" ]; then
  # Resolve the binary: PATH first, then the default install dir —
  # the same two-step pre-commit.sh uses.
  ethos_bin=""
  if command -v ethos >/dev/null 2>&1; then
    ethos_bin="ethos"
  elif [ -x "$HOME/.local/bin/ethos" ]; then
    ethos_bin="$HOME/.local/bin/ethos"
  fi
  if [ -n "$ethos_bin" ]; then
    # KEY=value lines, at most one of each. Strip whitespace so a CRLF
    # or a trailing space cannot end up inside a trailer value.
    #
    # stderr is deliberately NOT suppressed: a sidecar that will not
    # read, or a binary too old to know this subcommand, drops the
    # trailer, and a silently missing trailer is the failure class
    # this hook exists to prevent. The commit still proceeds.
    trailers=$("$ethos_bin" hook commit-trailers) || trailers=""
    MISSION_ID=$(printf '%s\n' "$trailers" | sed -n 's/^MISSION_ID=//p' | tr -d '[:space:]')
    DELEGATION_ID=$(printf '%s\n' "$trailers" | sed -n 's/^DELEGATION_ID=//p' | tr -d '[:space:]')
    export MISSION_ID DELEGATION_ID
  fi
fi
[ -z "${MISSION_ID:-}" ] && [ -z "${DELEGATION_ID:-}" ] && exit "$_host_status"
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
exit "$_host_status"
