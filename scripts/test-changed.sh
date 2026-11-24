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
    *.go)
      dir=.
      if [[ "$path" == */* ]]; then
        dir="${path%/*}"
      fi
      requested+=("$dir")
      ;;
    go.mod|go.sum|Makefile|.githooks/*|scripts/test-changed.sh|scripts/test-timed.sh|scripts/test-quarantine.py|tests/quarantine.tsv)
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
go test -race -count=1 "${packages[@]}"
