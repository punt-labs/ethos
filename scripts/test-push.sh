#!/usr/bin/env bash
set -euo pipefail

parallel=$1
packages=$(go list -race -f '{{.ImportPath}} {{.Module.Path}}' \
  ./internal/mission ./plugin/hooks ./internal/provenance ./test ./internal/gitenv ./...)
remaining=()
while read -r package module; do
  case "${package#"$module"/}" in
    cmd/ethos|internal/hook) ;;
    *) remaining+=("$package") ;;
  esac
done <<< "$packages"

flags=(-race -count=1 -timeout 30m)
pids=()
partition() {
  local package=$1 selector=$2 mode
  for mode in -run -skip; do
    go test "${flags[@]}" -p 1 "$mode" "$selector" "$package" &
    pids+=("$!")
  done
}

partition ./cmd/ethos '^Test(CLI|Mission)'
partition ./internal/hook '^TestGeneratedPostEditShell'
go test "${flags[@]}" -p "$parallel" "${remaining[@]}" &
pids+=("$!")

status=0
for pid in "${pids[@]}"; do
  wait "$pid" || status=$?
done
exit "$status"
