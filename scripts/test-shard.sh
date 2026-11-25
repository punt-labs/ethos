#!/usr/bin/env bash
set -euo pipefail

shard=$1
packages=$(go list -race -f '{{.ImportPath}} {{.Module.Path}}' ./...)
selected=()
while read -r package module; do
  case "${package#"$module"/}" in
    cmd/ethos|test) group=cli ;;
    internal/gitenv|plugin/hooks) group=git ;;
    internal/hook) group=hook ;;
    internal/enable|internal/mission) group=storage ;;
    *) group=remaining ;;
  esac
  if [[ "$group" == "$shard" ]]; then
    selected+=("$package")
  fi
done <<< "$packages"

if [[ ! ${selected[0]+selected} ]]; then
  printf 'No Go packages for shard %s\n' "$shard" >&2
  exit 1
fi
printf '%s\n' "${selected[@]}"
