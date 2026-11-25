#!/usr/bin/env bash
set -euo pipefail

git diff --cached --check
staged=$(mktemp)
trap 'rm -f "$staged"' EXIT
git diff --cached --name-only --no-renames -z > "$staged"

requested=()
while IFS= read -r -d '' path; do
  case "$path" in
    testdata/*)
      requested+=(.)
      ;;
    */testdata/*)
      requested+=("${path%%/testdata/*}")
      ;;
    internal/seed/sidecar/agents/*.md)
      requested+=(internal/seed)
      ;;
    scripts/legacy-seal.go)
      requested+=(test)
      ;;
    *.go)
      dir=.
      if [[ "$path" == */* ]]; then
        dir="${path%/*}"
      fi
      requested+=("$dir")
      ;;
    go.mod|go.sum|Makefile|.githooks/*|scripts/install-dev-hooks.py|scripts/test-changed.sh|scripts/test-smoke.tsv|scripts/test-timed.sh|scripts/test-quarantine.py|tests/quarantine.tsv)
      requested+=(test)
      ;;
  esac
done < "$staged"

if [[ ! ${requested[0]+selected} ]]; then
  echo "No staged Go packages to test."
  exit 0
fi

root=$(pwd -P)
git_env=$(git rev-parse --local-env-vars)
while IFS= read -r name; do
  unset "$name"
done <<< "$git_env"

dirs=$(go list -f '{{.Dir}}' ./...)
packages=()
while IFS= read -r dir; do
  for path in "${requested[@]}"; do
    if [[ "$dir" == "$root/$path" || ( "$path" == . && "$dir" == "$root" ) ]]; then
      packages+=("./$path")
      break
    fi
  done
done <<< "$dirs"

if [[ ! ${packages[0]+selected} ]]; then
  echo "No staged paths belong to a default Go package."
  exit 0
fi
fast=()
declare -a smoke_packages=() smoke_patterns=()
while IFS=$'\t' read -r smoke_package smoke_pattern || [[ -n "$smoke_package" ]]; do
  smoke_packages+=("$smoke_package")
  smoke_patterns+=("${smoke_pattern%$'\r'}")
done < "$(dirname -- "${BASH_SOURCE[0]}")/test-smoke.tsv"

for package in "${packages[@]}"; do
  for i in "${!smoke_packages[@]}"; do
    if [[ "$package" == "${smoke_packages[i]}" ]]; then
      status=0
      output=$(go test -race -count=1 -v -run "^Test(${smoke_patterns[i]})$" "$package" 2>&1) || status=$?
      printf '%s\n' "$output"
      if (( status != 0 )); then
        exit "$status"
      fi
      if [[ $'\n'"$output" != *$'\n=== RUN   Test'* ]]; then
        printf 'No smoke tests matched for %s\n' "$package" >&2
        exit 1
      fi
      continue 2
    fi
  done
  fast+=("$package")
done
if [[ ${fast[0]+selected} ]]; then
  go test -race -count=1 "${fast[@]}"
fi
