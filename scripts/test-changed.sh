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
    go.mod|go.sum|Makefile|.githooks/*|scripts/install-dev-hooks.py|scripts/test-changed.sh|scripts/test-timed.sh|scripts/test-quarantine.py|tests/quarantine.tsv)
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
for package in "${packages[@]}"; do
  case "$package" in
    ./cmd/ethos) smoke='Slugify|VersionCommand|VersionCommandJSON' ;;
    ./internal/enable) smoke='EnableDepositsFourArtifacts|EnableDisableRoundTrip' ;;
    ./internal/gitenv) smoke='Parsed|TimeoutDefault' ;;
    ./internal/hook) smoke='BuildPersonaBlock_Full|BuildExtensionContext' ;;
    ./internal/mission) smoke='Store_RoundTrip|Store_RejectsSymlink_Load' ;;
    ./plugin/hooks) smoke='HooksDormantRepositoryRunsNoProbe|HooksFailLoudWhenRevParseFails' ;;
    *) fast+=("$package"); continue ;;
  esac
  go test -race -count=1 -run "^Test($smoke)$" "$package"
done
if [[ ${fast[0]+selected} ]]; then
  go test -race -count=1 "${fast[@]}"
fi
