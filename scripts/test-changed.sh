#!/usr/bin/env bash
set -euo pipefail

staged=${ETHOS_STAGED_PATHS:?run make test-changed}
requested=()
all=
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
    go.mod|go.sum)
      all=1
      ;;
    Makefile|.githooks/*|scripts/install-dev-hooks.py|scripts/test-changed.sh|scripts/test-smoke.tsv|scripts/test-timed.sh|scripts/test-snapshot.sh|scripts/test-kind.py|scripts/test-quarantine.py|tests/go-subprocess-tests.txt|tests/quarantine.tsv)
      requested+=(test)
      ;;
  esac
done < "$staged"

if [[ -z "$all" && ! ${requested[0]+selected} ]]; then
  echo "No staged Go packages to test."
  exit 0
fi

root=$(pwd -P)

graph=$(go list -race -f '{{.ImportPath}}|{{.Dir}}|{{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}|{{with .Module}}{{.Path}}{{end}}' ./...)
imports=() dirs=() dependencies=() selected=()
while IFS='|' read -r import dir deps module; do
  [[ -n "$import" ]] || continue
  imports+=("$import")
  dirs+=("$dir")
  dependencies+=(" $deps ")
  selected+=("$all")
  i=$(("${#imports[@]}" - 1))
  for path in ${requested[@]+"${requested[@]}"}; do
    affected=$module
    if [[ "$path" != . ]]; then
      affected+="/$path"
    fi
    if [[ "$dir" == "$root/$path" || ( "$path" == . && "$dir" == "$root" ) ||
      ( -n "$module" && "${dependencies[i]}" == *" $affected "* ) ]]; then
      selected[i]=1
      break
    fi
  done
done <<< "$graph"

if [[ ${imports[0]+listed} ]]; then
  changed=1
  while [[ -n "$changed" ]]; do
    changed=
    for i in "${!imports[@]}"; do
      [[ -z "${selected[i]}" ]] || continue
      for j in "${!imports[@]}"; do
        if [[ -n "${selected[j]}" && "${dependencies[i]}" == *" ${imports[j]} "* ]]; then
          selected[i]=1
          changed=1
          break
        fi
      done
    done
  done
fi

packages=()
if [[ ${imports[0]+listed} ]]; then
  for i in "${!imports[@]}"; do
    [[ -n "${selected[i]}" ]] || continue
    if [[ "${dirs[i]}" == "$root" ]]; then
      packages+=(.)
    else
      packages+=("./${dirs[i]#"$root/"}")
    fi
  done
fi
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
