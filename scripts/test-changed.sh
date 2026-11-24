#!/usr/bin/env bash
set -euo pipefail

git diff --cached --check
staged=$(mktemp)
trap 'rm -f "$staged"' EXIT
git diff --cached --name-only --no-renames -z > "$staged"

requested=()
while IFS= read -r -d '' path; do
  case "$path" in
    *.go)
      dir=.
      if [[ "$path" == */* ]]; then
        dir="${path%/*}"
      fi
      requested+=("$dir")
      ;;
    go.mod|go.sum|Makefile|.githooks/*|scripts/test-changed.sh)
      requested+=(test)
      ;;
  esac
done < "$staged"

if (("${#requested[@]}" == 0)); then
  echo "No staged Go packages to test."
  exit 0
fi

dirs=$(go list -f '{{.Dir}}' ./...)
packages=()
while IFS= read -r dir; do
  for path in "${requested[@]}"; do
    if [[ "$dir" == "$(pwd -P)/$path" || ( "$path" == . && "$dir" == "$(pwd -P)" ) ]]; then
      packages+=("./$path")
      break
    fi
  done
done <<< "$dirs"

if (("${#packages[@]}" == 0)); then
  echo "No staged paths belong to a default Go package."
  exit 0
fi
go test -race -count=1 "${packages[@]}"
