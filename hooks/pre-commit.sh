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
# deferral, ethos not installed) is the captured host status $_host_status:
# 0 when this runs standalone (git starts the hook with $? = 0), or the host
# hook's fall-through status when this is chained after a foreign hook. So a
# clean seal is transparent — it never turns a host's failing fall-through
# into a passing commit, and never blocks a commit the host would have passed.
#
# Passthrough when ethos is not installed — a missing binary must never
# block a commit in an unrelated repo.

# Preserve a chained host hook's fall-through status. When install.sh appends
# this script after a foreign hook's content, $? here is that hook's last
# command status; returning it on passthrough keeps chaining from masking a
# host hook that signals failure by fall-through. Standalone, git invokes us
# fresh with $? = 0, so this is exit 0 as before. Captured before any command
# runs (a later assignment would reset $?).
_host_status=$?

# Resolve the ethos binary: PATH first, then the default install dir.
ethos_bin=""
if command -v ethos >/dev/null 2>&1; then
  ethos_bin="ethos"
elif [ -x "$HOME/.local/bin/ethos" ]; then
  ethos_bin="$HOME/.local/bin/ethos"
else
  exit "$_host_status"
fi

# `ethos audit seal` is fail-closed: exit 2 on an I/O error, a malformed or
# corrupt chunk, or a git-add failure. Propagate any nonzero as a blocking
# exit 2 so a broken audit store cannot slip an unrecorded commit through.
if ! "$ethos_bin" audit seal; then
  exit 2
fi
exit "$_host_status"
