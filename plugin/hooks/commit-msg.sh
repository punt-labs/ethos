#!/bin/sh
# hooks/commit-msg.sh — Append Mission:/Delegation: git trailers when env is
# set, or resolved from the committing session when it is not.
# DES-054 phase 3: connects git history to the audited delegation chain so
# `git log --grep Mission:` becomes a forensic search tool. MISSION_ID/
# DELEGATION_ID are the uncommon case (subagent commits don't inherit
# additional_env), so the common path below shells to `ethos hook
# commit-trailers`, which resolves the committing session itself
# (ETHOS_SESSION, else the Claude process-tree walk) and reads its
# active-mission and delegation-binding sidecars. Per DES-074, that
# resolution returns a named, printed error when running under Claude Code
# and the session cannot be identified, and stays silent only outside Claude
# Code, where no session was ever expected. The lookup is skipped only when
# at least one of MISSION_ID/DELEGATION_ID is already in env; a commit gets
# no trailer, and no lookup, only when the env vars are unset AND the
# session lookup finds nothing to add.
#
# Idempotency: re-running on a message already carrying the trailer leaves
# it unchanged. The CLI renders with git-interpret-trailers in memory, then
# atomically installs from canonical store-local staging. Any failure blocks;
# there is no adjacent staging or plain-append fallback.
#
# Preserve a chained host hook's fall-through status: when install.sh appends
# this script after a foreign commit-msg hook, $? here is that hook's last
# command status; every passthrough returns it so chaining never masks a host
# hook that signals failure by fall-through. Standalone, $? = 0 as before.
_host_status=$?

# _git runs git with every trace variable cleared and trace2 switched off by the
# environment (which overrides a target set in the user's config): a trace on
# stdout, from `trace2.normalTarget=/dev/stdout` or GIT_TRACE=/dev/stdout, would
# otherwise become part of what the hook reads or writes.
_git() {
  (
    for _v in $(env | sed -n 's/^\(GIT_TRACE[A-Za-z0-9_]*\)=.*/\1/p'); do
      unset "$_v"
    done
    unset GIT_CURL_VERBOSE
    GIT_TRACE=0 GIT_TRACE2=0 GIT_TRACE2_PERF=0 GIT_TRACE2_EVENT=0
    export GIT_TRACE GIT_TRACE2 GIT_TRACE2_PERF GIT_TRACE2_EVENT
    exec git "$@"
  )
}

# Dormant fast path. git runs a hook at the top of the work tree, so the marker
# is tested relative to the current directory first, before git runs:
# a dormant repository is never blocked by a probe failure. The shortcut is
# taken only when this directory has its own .git (it is a top level, so the
# marker there is the marker that counts) and every path component is either
# absent or a plain readable directory. Anywhere else (a hook run by hand from
# a subdirectory, a link, an unreadable store) the full resolution below
# decides and reports as before.
_ethos_dormant=1
for _component in .punt-labs .punt-labs/ethos; do
  if [ -L "$_component" ]; then
    _ethos_dormant=""
  elif [ -e "$_component" ] && { [ ! -d "$_component" ] || [ ! -r "$_component" ] || [ ! -x "$_component" ]; }; then
    _ethos_dormant=""
  fi
done
if [ -e .punt-labs/ethos/enabled ] || [ -L .punt-labs/ethos/enabled ]; then
  _ethos_dormant=""
fi
if [ -n "$_ethos_dormant" ] && [ -e .git ]; then
  exit "$_host_status"
fi

# §2.7 marker gate: ethos does no commit-time work unless it is enabled in
# this repo. REPO_ROOT is resolved inside the hook (worktree-safe), not baked
# in at install time. Absent marker → exit with the captured host status,
# never a bare exit 0, so a chained host that signals failure by fall-through
# still blocks the commit even when ethos is dormant.
# A git that fails here (dubious ownership, a corrupt .git) is not "ethos is
# dormant": the hook prints git's own error and exits 1, so the commit is
# blocked and the operator sees why, rather than skipping with the host status.
# Capture only stdout. Git's diagnostics remain directly visible, including
# success warnings, without polluting the root or creating temporary files.
# Probe once: a second lookup could answer for a different repository state.
if ! REPO_ROOT=$(_git rev-parse --show-toplevel); then
  printf 'ethos: commit-msg: git rev-parse --show-toplevel failed\n' >&2
  exit 1
fi
# The root must be one absolute line (a / path, or a Windows drive path under
# MINGW, MSYS or CYGWIN) naming a
# directory; anything else (a trace line that got through, a relative path) is
# not a root to trust.
case "$REPO_ROOT" in
/*) ;;
[A-Za-z]:/*)
  # A drive path is a root only where git for Windows runs this shell.
  case "$(uname -s 2>/dev/null)" in
  MINGW* | MSYS* | CYGWIN*) ;;
  *) _revparse_bad=1 ;;
  esac
  ;;
*) _revparse_bad=1 ;;
esac
case "$REPO_ROOT" in
*"
"*) _revparse_bad=1 ;;
esac
if [ -n "${_revparse_bad:-}" ] || [ ! -d "$REPO_ROOT" ]; then
  printf 'ethos: commit-msg: git rev-parse --show-toplevel did not print one absolute directory: %s\n' "$REPO_ROOT" >&2
  exit 1
fi
# "Marker absent" means only that every component of the path exists and the
# marker file does not. A .punt-labs or .punt-labs/ethos that is a dangling
# link, is not a directory, or cannot be searched is a store ethos cannot read:
# say so and block, never treat it as dormant.
_ethos_path="$REPO_ROOT/.punt-labs"
for _component in "$_ethos_path" "$_ethos_path/ethos" "$_ethos_path/ethos/enabled"; do
  if [ -L "$_component" ] && [ ! -e "$_component" ]; then
    printf 'ethos: commit-msg: %s is a dangling link; repair or remove it\n' "$_component" >&2
    exit 1
  fi
  [ -e "$_component" ] || break
  case "$_component" in
  */enabled)
    if [ ! -f "$_component" ]; then
      printf 'ethos: commit-msg: the enabled marker %s is not a regular file\n' "$_component" >&2
      exit 1
    fi
    ;;
  *)
    if [ ! -d "$_component" ]; then
      printf 'ethos: commit-msg: %s is not a directory\n' "$_component" >&2
      exit 1
    fi
    if [ ! -r "$_component" ] || [ ! -x "$_component" ]; then
      printf 'ethos: commit-msg: %s cannot be read\n' "$_component" >&2
      exit 1
    fi
    ;;
  esac
done
[ -f "$REPO_ROOT/.punt-labs/ethos/enabled" ] || exit "$_host_status"

# Resolve the binary: PATH first, then the default install dir — the same two
# steps pre-commit.sh uses. Enabled here and no binary: the commit would carry
# no trailers and, if this hook runs alone (`git merge --no-ff` runs commit-msg
# and not pre-commit), unsealed audit lines. Say so and block.
ethos_bin=""
if command -v ethos >/dev/null 2>&1; then
  ethos_bin="ethos"
elif [ -x "$HOME/.local/bin/ethos" ]; then
  ethos_bin="$HOME/.local/bin/ethos"
else
  printf 'ethos: commit-msg: ethos is enabled here but not installed; the commit is not sealed\n' >&2
  exit 1
fi

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
  if [ -n "$ethos_bin" ]; then
    # KEY=value lines, at most one of each. Strip whitespace so a CRLF
    # or a trailing space cannot end up inside a trailer value.
    #
    # stderr is deliberately NOT suppressed: a sidecar that will not
    # read, or a binary too old to know this subcommand, drops the
    # trailer, and a silently missing trailer is the failure class
    # this hook exists to prevent. The commit still proceeds.
    trailers=$("$ethos_bin" hook commit-trailers) || trailers=""
    # An ethos predating `hook commit-trailers` prints the hook group's
    # HELP on stdout and exits 0, so the `||` above never fires and the
    # trailer vanishes without a word. Treat help-shaped output as the
    # failure it is and say so. Newer binaries pin `hook` to NoArgs and
    # exit non-zero, so this guard only fires on a version mismatch.
    case "$trailers" in
    *"Usage:"*)
      printf 'ethos: commit-msg: %s does not support "hook commit-trailers"; upgrade ethos to restore Mission/Delegation trailers\n' \
        "$ethos_bin" >&2
      trailers=""
      ;;
    esac
    MISSION_ID=$(printf '%s\n' "$trailers" | sed -n 's/^MISSION_ID=//p' | tr -d '[:space:]')
    DELEGATION_ID=$(printf '%s\n' "$trailers" | sed -n 's/^DELEGATION_ID=//p' | tr -d '[:space:]')
    export MISSION_ID DELEGATION_ID
  fi
fi
[ -z "${MISSION_ID:-}" ] && [ -z "${DELEGATION_ID:-}" ] && exit "$_host_status"
# The helper validates IDs, renders every missing trailer before any write,
# and stages only under S's local root, independently of the caller's TMPDIR.
# Successful helpers are silent; reject help from an obsolete binary too.
message_result=$("$ethos_bin" hook commit-message "$msg_file") || exit 1
if [ -n "$message_result" ]; then
  printf 'ethos: commit-msg: unexpected output from "hook commit-message"; upgrade ethos\n' >&2
  exit 1
fi
exit "$_host_status"
