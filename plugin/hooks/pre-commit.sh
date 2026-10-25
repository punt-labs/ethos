#!/bin/sh
# hooks/pre-commit.sh — Seal pending live audit lines into tracked chunks
# (DES-058). Runs before git snapshots the index, so the freshly staged
# chunks land in the SAME commit as the work they document.
#
# DES-055 shape: on a seal failure the underlying `ethos audit seal` prints
# a self-contained remedy to stderr and exits 2; this hook propagates that
# as a blocking exit 2.
#
# Exit status on the non-failure paths (success, nothing-to-seal, gitlink
# deferral) is the captured host status $_host_status:
# 0 when this runs standalone (git starts the hook with $? = 0), or the host
# hook's fall-through status when this is chained after a foreign hook. So a
# clean seal is transparent — it never turns a host's failing fall-through
# into a passing commit, and never blocks a commit the host would have passed.
#
# Passthrough when ethos is not enabled in the repo — a missing binary must
# never block a commit in an unrelated repo. Where ethos IS enabled, a missing
# binary blocks: the commit would carry unsealed audit lines.

# Preserve a chained host hook's fall-through status. When install.sh appends
# this script after a foreign hook's content, $? here is that hook's last
# command status; returning it on passthrough keeps chaining from masking a
# host hook that signals failure by fall-through. Standalone, git invokes us
# fresh with $? = 0, so this is exit 0 as before. Captured before any command
# runs (a later assignment would reset $?).
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

# §2.7 marker gate: ethos does no commit-time work unless it is enabled in
# this repo. REPO_ROOT is resolved inside the hook (worktree-safe: a worktree
# resolves to its own work-tree root, where its .punt-labs/ethos/ lives), not
# baked in at install time. Absent marker → exit with the captured host status,
# never a bare exit 0, so a chained host that signals failure by fall-through
# still blocks the commit even when ethos is dormant. The gate sits before the
# binary is ever resolved, so a dormant repo does no ethos work at commit time.
# A git that fails here (dubious ownership, a corrupt .git) is not "ethos is
# dormant": the hook prints git's own error and exits 1, so the commit is
# blocked and the operator sees why, rather than skipping with the host status.
# git's stderr goes to a file and is shown only on failure: a warning or
# GIT_TRACE output on a rev-parse that succeeded must not become part of the
# repository root, nor make the hook skip the seal.
# A mktemp that fails does not block: rev-parse then runs without the capture
# file, and is run again to show git's error only if it failed. A dormant
# repository must not be blocked by a full or read-only temp directory.
_revparse_err=$(mktemp 2>/dev/null) || _revparse_err=""
if [ -n "$_revparse_err" ]; then
  REPO_ROOT=$(_git rev-parse --show-toplevel 2>"$_revparse_err")
else
  REPO_ROOT=$(_git rev-parse --show-toplevel 2>/dev/null)
fi
_revparse_status=$?
if [ "$_revparse_status" -ne 0 ]; then
  if [ -n "$_revparse_err" ]; then
    _revparse_msg=$(cat "$_revparse_err")
    rm -f "$_revparse_err"
  else
    _revparse_msg=$(_git rev-parse --show-toplevel 2>&1 >/dev/null)
  fi
  printf 'ethos: pre-commit: git rev-parse --show-toplevel failed: %s\n' "$_revparse_msg" >&2
  exit 1
fi
[ -n "$_revparse_err" ] && rm -f "$_revparse_err"
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
  printf 'ethos: pre-commit: git rev-parse --show-toplevel did not print one absolute directory: %s\n' "$REPO_ROOT" >&2
  exit 1
fi
# "Marker absent" means only that every component of the path exists and the
# marker file does not. A .punt-labs or .punt-labs/ethos that is a dangling
# link, is not a directory, or cannot be searched is a store ethos cannot read:
# say so and block, never treat it as dormant.
_ethos_path="$REPO_ROOT/.punt-labs"
for _component in "$_ethos_path" "$_ethos_path/ethos" "$_ethos_path/ethos/enabled"; do
  if [ -L "$_component" ] && [ ! -e "$_component" ]; then
    printf 'ethos: pre-commit: %s is a dangling link; repair or remove it\n' "$_component" >&2
    exit 1
  fi
  [ -e "$_component" ] || break
  case "$_component" in
  */enabled)
    if [ ! -f "$_component" ]; then
      printf 'ethos: pre-commit: the enabled marker %s is not a regular file\n' "$_component" >&2
      exit 1
    fi
    ;;
  *)
    if [ ! -d "$_component" ]; then
      printf 'ethos: pre-commit: %s is not a directory\n' "$_component" >&2
      exit 1
    fi
    if [ ! -r "$_component" ] || [ ! -x "$_component" ]; then
      printf 'ethos: pre-commit: %s cannot be read\n' "$_component" >&2
      exit 1
    fi
    ;;
  esac
done
[ -f "$REPO_ROOT/.punt-labs/ethos/enabled" ] || exit "$_host_status"

# Resolve the ethos binary: PATH first, then the default install dir.
ethos_bin=""
if command -v ethos >/dev/null 2>&1; then
  ethos_bin="ethos"
elif [ -x "$HOME/.local/bin/ethos" ]; then
  ethos_bin="$HOME/.local/bin/ethos"
else
  # Enabled here, and no binary: the commit would carry audit lines that were
  # never sealed. Say so and block, rather than pass quietly.
  printf 'ethos: pre-commit: ethos is enabled here but not installed; the commit is not sealed\n' >&2
  exit 1
fi

# `ethos audit seal` is fail-closed: exit 2 on an I/O error, a malformed or
# corrupt chunk, or a git-add failure. Propagate any nonzero as a blocking
# exit 2 so a broken audit store cannot slip an unrecorded commit through.
if ! "$ethos_bin" audit seal; then
  exit 2
fi
exit "$_host_status"
