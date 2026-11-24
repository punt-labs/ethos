#!/usr/bin/env bash
set -euo pipefail

tier=$1
budget=$2
shift 2

SECONDS=0
status=0
"$@" || status=$?
elapsed=$SECONDS
verdict=within
if (( elapsed >= budget )); then
  verdict=over
fi
printf '%s: %ss elapsed (%s %ss budget), exit %s\n' "$tier" "$elapsed" "$verdict" "$budget" "$status"
exit "$status"
