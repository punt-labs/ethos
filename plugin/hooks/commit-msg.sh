#!/bin/sh
# hooks/commit-msg.sh — Append Mission:/Delegation: git trailers at commit time.
# Thin shim (DES-083): finds the binary and delegates; policy lives in
# `ethos hook commit-msg`. Passes through unless ethos is enabled here.
_host_status=$?
_hook=commit-msg
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
if ! REPO_ROOT=$(_git rev-parse --show-toplevel); then
  printf 'ethos: %s: git rev-parse --show-toplevel failed\n' "$_hook" >&2
  exit 1
fi
case "$REPO_ROOT" in
/*) ;;
[A-Za-z]:/*)
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
  printf 'ethos: %s: git rev-parse --show-toplevel did not print one absolute directory: %s\n' "$_hook" "$REPO_ROOT" >&2
  exit 1
fi
_ethos_path="$REPO_ROOT/.punt-labs"
for _component in "$_ethos_path" "$_ethos_path/ethos" "$_ethos_path/ethos/enabled"; do
  if [ -L "$_component" ] && [ ! -e "$_component" ]; then
    printf 'ethos: %s: %s is a dangling link; repair or remove it\n' "$_hook" "$_component" >&2
    exit 1
  fi
  [ -e "$_component" ] || break
  case "$_component" in
  */enabled)
    if [ ! -f "$_component" ]; then
      printf 'ethos: %s: the enabled marker %s is not a regular file\n' "$_hook" "$_component" >&2
      exit 1
    fi
    ;;
  *)
    if [ ! -d "$_component" ] || [ ! -r "$_component" ] || [ ! -x "$_component" ]; then
      printf 'ethos: %s: %s is not a readable directory\n' "$_hook" "$_component" >&2
      exit 1
    fi
    ;;
  esac
done
[ -f "$REPO_ROOT/.punt-labs/ethos/enabled" ] || exit "$_host_status"
if command -v ethos >/dev/null 2>&1; then
  ethos_bin="ethos"
elif [ -x "$HOME/.local/bin/ethos" ]; then
  ethos_bin="$HOME/.local/bin/ethos"
else
  printf 'ethos: %s: ethos is enabled here but not installed; the commit is not sealed\n' "$_hook" >&2
  exit 1
fi
[ -z "$1" ] && exit "$_host_status"
[ -f "$1" ] || exit "$_host_status"
# The subcommand prints nothing on stdout. A binary before v4.9 does not know
# it and answers with the hook group's help on stdout and exit 0, so output is
# a failure too, never a pass.
# Command substitution strips trailing newlines, so a binary that prints only
# newlines would read as silent. Append the exit status after a marker byte and
# split it off again: the output keeps every byte it printed.
_cap=$("$ethos_bin" hook commit-msg "$1"; printf 'x%s' "$?")
_rc=${_cap##*x}
_out=${_cap%x*}
if [ "$_rc" -ne 0 ] || [ -n "$_out" ]; then
  if ! "$ethos_bin" hook commit-msg --help 2>/dev/null | grep -q 'ethos hook commit-msg'; then
    # A binary that predates the subcommand: its --help never names it. Its
    # stdout is the group help, which would only bury the remedy; anything
    # else is a diagnostic the operator needs before the remedy.
    case "$_out" in
    *"ethos hook [command]"*) ;;
    *) [ -n "$_out" ] && printf '%s\n' "$_out" >&2 ;;
    esac
    printf 'ethos: %s: %s does not support "hook commit-msg"; upgrade ethos: make install (or run install.sh)\n' "$_hook" "$ethos_bin" >&2
  else
    [ -n "$_out" ] && printf '%s\n' "$_out" >&2
    # Exit 0 with output is a broken binary, not a policy block that explained itself.
    [ "$_rc" -eq 0 ] &&
      printf 'ethos: %s: unexpected output from "ethos hook commit-msg"; the commit is blocked\n' "$_hook" >&2
  fi
  exit 2
fi
exit "$_host_status"
