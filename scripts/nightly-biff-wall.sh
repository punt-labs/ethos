#!/usr/bin/env bash
set -euo pipefail

: "${BIFF_RELAY_TOKEN:?BIFF_RELAY_TOKEN is required to deliver Nightly alerts}"
result=$1
run=$2
attempt=$3
run_prefix="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/"
run_url="${run_prefix}${run}/attempts/${attempt}"
message="Nightly QA $result on Ethos — $run_url"
biff=(uvx --from punt-biff==1.24.2 biff --user github-actions --json)

if [[ ${ALERT_DRILL:-false} == true ]]; then
  printf 'peers:\n  orgs: []\n  repos: []\n' > .punt-labs/biff/config.local.yaml
  message="DRILL: $message"
fi
current=$("${biff[@]}" wall)
if [[ ${ALERT_DRILL:-false} == true ]] &&
    ! jq -e '(.status == "inactive" and has("wall") and .wall == null)
             or (.text | strings | startswith("DRILL: Nightly QA "))' <<< "$current" >/dev/null; then
  echo "::error::Nightly drill cannot replace an active non-drill wall or verify that the wall is safe to replace"
  exit 1
fi

greater_decimal() {
  (( ${#1} > ${#2} )) || { (( ${#1} == ${#2} )) && [[ $1 > $2 ]]; }
}

current_text=$(jq -r '.text // empty' <<< "$current")
if [[ ${ALERT_DRILL:-false} == true ]]; then
  current_text=${current_text#DRILL: }
fi
current_path=
for state in failure cancelled; do
  prefix="Nightly QA $state on Ethos — $run_prefix"
  if [[ $current_text == "$prefix"* ]]; then
    current_path=${current_text#"$prefix"}
    break
  fi
done
if [[ $current_path =~ ^([1-9][0-9]*)/attempts/([1-9][0-9]*)$ ]]; then
  current_run=${BASH_REMATCH[1]}
  current_attempt=${BASH_REMATCH[2]}
  if greater_decimal "$current_run" "$run" ||
      { [[ $current_run == "$run" ]] && greater_decimal "$current_attempt" "$attempt"; }; then
    echo "Skipping $run_url: Biff wall already shows newer Nightly $run_prefix$current_path"
    exit 0
  fi
fi

"${biff[@]}" wall "$message" --duration 24h
current=$("${biff[@]}" wall)
if ! jq -e --arg message "$message" '.text == $message' <<< "$current" >/dev/null; then
  echo "::error::Biff wall readback did not match the Nightly notification"
  exit 1
fi
