#!/usr/bin/env bash
set -euo pipefail

mode=$1
shift
root=$(git rev-parse --show-toplevel)
scratch=$(mktemp -d)
scratch=$(cd "$scratch" && pwd -P)
snapshot=$scratch/checkout
git_env=$(git rev-parse --local-env-vars)
added=
cleanup() {
  if [[ -n "$added" ]]; then
    git -C "$root" worktree remove --force "$snapshot" || return
  fi
  rm -rf "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

case "$mode" in
  staged)
    git diff --cached --check
    git diff --cached --name-only --no-renames -z > "$scratch/paths"
    export ETHOS_STAGED_PATHS="$scratch/paths"
    tree=$(git write-tree)
    parent=()
    if head=$(git rev-parse --verify HEAD 2>/dev/null); then
      parent=(-p "$head")
    fi
    commit=$(GIT_AUTHOR_NAME=Ethos GIT_AUTHOR_EMAIL=ethos@example.invalid \
      GIT_COMMITTER_NAME=Ethos GIT_COMMITTER_EMAIL=ethos@example.invalid \
      git -c commit.gpgSign=false commit-tree "$tree" ${parent[@]+"${parent[@]}"} -m 'Ethos staged test snapshot')
    ;;
  commit)
    commit=$1
    shift
    ;;
  *)
    printf 'Unknown test snapshot mode: %s\n' "$mode" >&2
    exit 2
    ;;
esac

while IFS= read -r name; do
  unset "$name"
done <<< "$git_env"

git -C "$root" -c core.hooksPath=/dev/null worktree add --quiet --detach "$snapshot" "$commit"
added=1
cd "$snapshot"
GOWORK=off "$@"
