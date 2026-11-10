#!/usr/bin/env bash
# scripts/legacy_corpus_test.sh -- check the committed legacy corpus is
# reproducible and clean.
#
# 1. Regenerates one version (default v4.22.1, the cheapest build to keep warm)
#    into a temporary directory with scripts/legacy-corpus.sh and compares it to
#    the committed internal/autorepair/testdata/corpus/<tag>/: the file list must
#    be identical and the contents identical after timestamps, dates, hashes and
#    other generated ids are normalized.
# 2. Scans every committed corpus and damage file for a real home path, a
#    non-example email, and any secret-shaped string other than the fake token.
# 3. Checks every damage fixture has a README line and the total size is under 2 MB.
#
# Usage: scripts/legacy_corpus_test.sh [TAG]
# Needs full tag history for TAG (git fetch --tags); a missing tag is a failure,
# not a skip, because a silent skip would let the corpus rot.
# Not part of `make check` (it builds a binary from a tag); run `make test-legacy-corpus`.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
TAG=${1:-v4.22.1}
TESTDATA=$REPO_ROOT/internal/autorepair/testdata
FAKE_TOKEN="ghp_""FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"

failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }
pass() { echo "ok: $*"; }

# normalize rewrites the generated parts of a file or path so two runs compare equal.
normalize() {
  perl -pe '
    s/\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z/TIME/g;
    s/\d{4}-\d{2}-\d{2}/DATE/g;
    s/\b[0-9a-f]{64}\b/HASH64/g;
    s/\b[0-9a-f]{32}\b/ID32/g;
    s/\b\d{13,}\b/NANOS/g;
  '
}

[ -d "$TESTDATA/corpus/$TAG" ] || { echo "FAIL: no committed corpus for $TAG under $TESTDATA/corpus" >&2; exit 1; }

TMPROOT=${TMPDIR:-$REPO_ROOT/.tmp}
mkdir -p "$TMPROOT"
WORKDIR=$(mktemp -d "${TMPROOT%/}/legacy-corpus-test.XXXXXX")
trap 'rm -rf "$WORKDIR"' EXIT

# --- 1. reproducibility ---------------------------------------------------
# LEGACY_CORPUS_SKIP_REGEN=1 skips only this multi-minute rebuild (for iterating
# on the static checks below); a full run never sets it.
if [ -n "${LEGACY_CORPUS_SKIP_REGEN:-}" ]; then
  :
elif ! "$SCRIPT_DIR/legacy-corpus.sh" --versions "$TAG" --out "$WORKDIR/out" --work "$WORKDIR/work" --no-damage --no-readme >"$WORKDIR/gen.log" 2>&1; then
  cat "$WORKDIR/gen.log" >&2
  fail "regenerating $TAG failed"
else
  want=$TESTDATA/corpus/$TAG
  got=$WORKDIR/out/corpus/$TAG
  ( cd "$want" && find . -type f | LC_ALL=C sort | normalize ) >"$WORKDIR/want.list"
  ( cd "$got" && find . -type f | LC_ALL=C sort | normalize ) >"$WORKDIR/got.list"
  if diff -u "$WORKDIR/want.list" "$WORKDIR/got.list" >"$WORKDIR/list.diff"; then
    pass "$TAG: regenerated file list matches the committed corpus ($(wc -l <"$WORKDIR/want.list" | tr -d ' ') files)"
  else
    cat "$WORKDIR/list.diff" >&2
    fail "$TAG: regenerated file list differs from the committed corpus"
  fi
  differing=""
  while IFS= read -r f; do
    [ -f "$got/$f" ] || continue
    # The bundle and index are binary and carry object ids and stat data; the
    # tracked path set beside them is what is compared.
    case $f in ./git/repo.bundle|./git/index|./git/HEAD) continue ;; esac
    if ! cmp -s <(normalize <"$want/$f") <(normalize <"$got/$f"); then differing="$differing $f"; fi
  done < <(cd "$want" && find . -type f | LC_ALL=C sort)
  if [ -z "$differing" ]; then
    pass "$TAG: contents match after normalizing times, dates, hashes and ids"
  else
    fail "$TAG: contents differ beyond timestamps and ids:$differing"
  fi
fi

# --- 1b. committed snapshots are complete ----------------------------------
# Every path a snapshot's git/tracked-files.txt lists must exist under repo/, and
# git ls-files under repo/ must equal the union of that list and the files on
# disk. The root .gitignore drops .ethos/, *.create.lock and others, so a file
# that was never `git add -f`'d is missing from a clean clone yet still named in
# tracked-files.txt and the bundle; this fails for every tag, not just TAG.
for snap in "$TESTDATA"/corpus/*/; do
  v=$(basename "$snap")
  missing=""
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    [ -e "$snap/repo/$line" ] || [ -L "$snap/repo/$line" ] || missing="$missing $line"
  done <"$snap/git/tracked-files.txt"
  [ -z "$missing" ] || fail "$v: tracked-files.txt lists paths absent from repo/:$missing"
  { cat "$snap/git/tracked-files.txt"; ( cd "$snap/repo" && find . \( -type f -o -type l \) | sed 's|^\./||' ); } | LC_ALL=C sort -u >"$WORKDIR/want.set"
  git -C "$REPO_ROOT" ls-files -- "internal/autorepair/testdata/corpus/$v/repo" | sed "s|^internal/autorepair/testdata/corpus/$v/repo/||" | LC_ALL=C sort -u >"$WORKDIR/have.set"
  if ! diff -u "$WORKDIR/want.set" "$WORKDIR/have.set" >"$WORKDIR/set.diff"; then
    cat "$WORKDIR/set.diff" >&2
    fail "$v: git ls-files under repo/ differs from the snapshot's file set (a force-add is missing; use git add -f)"
  fi
done
[ "$failures" -ne 0 ] || pass "every snapshot's tracked-files.txt exists under repo/ and is committed"

# --- 2. privacy -----------------------------------------------------------
scan_fail() { fail "privacy: $1"; }
hits=$(grep -rIlE '/(Users|home)/[A-Za-z][A-Za-z0-9._-]*' "$TESTDATA/corpus" "$TESTDATA/damage" 2>/dev/null || true)
[ -z "$hits" ] || scan_fail "absolute home path in: $hits"
if [ -n "${HOME:-}" ]; then
  hits=$(grep -rIlF "$HOME" "$TESTDATA/corpus" "$TESTDATA/damage" 2>/dev/null || true)
  [ -z "$hits" ] || scan_fail "this machine's HOME in: $hits"
fi
user=$(id -un 2>/dev/null || true)
if [ "${#user}" -ge 5 ]; then
  hits=$(grep -rIlw "$user" "$TESTDATA" 2>/dev/null || true)
  [ -z "$hits" ] || scan_fail "user name in: $hits"
fi
hits=$(grep -rIhoE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' "$TESTDATA" 2>/dev/null | LC_ALL=C sort -u | grep -Ev '@example\.com$' || true)
[ -z "$hits" ] || scan_fail "non-example email(s): $(echo "$hits" | tr '\n' ' ')"
while IFS= read -r f; do
  if grep -E 'gh[pousr]_[A-Za-z0-9]{36,}' "$f" | grep -qv "$FAKE_TOKEN"; then scan_fail "secret-shaped string other than the fake token in $f"; fi
done < <(grep -rIlE 'gh[pousr]_[A-Za-z0-9]{36,}' "$TESTDATA" 2>/dev/null || true)
hits=$(grep -rIlE 'AKIA[0-9A-Z]{16}|BEGIN [A-Z ]*PRIVATE KEY' "$TESTDATA" 2>/dev/null || true)
[ -z "$hits" ] || scan_fail "key material in: $hits"
if [ "$failures" -eq 0 ]; then pass "privacy: no real home path, user name, non-example email, or other secret-shaped string"; fi

# --- 3. damage fixtures and size -----------------------------------------
dmg=0
for d in "$TESTDATA"/damage/*/; do
  dmg=$((dmg + 1))
  [ -s "$d/README" ] || fail "damage fixture $(basename "$d") has no README line"
done
[ "$dmg" -ge 11 ] || fail "expected at least 11 damage fixtures, found $dmg"
bytes=$( { find "$TESTDATA/corpus" "$TESTDATA/damage" -type f -exec wc -c {} + || true; } | awk '$2=="total"{t+=$1} END{print t+0}')
if [ "$bytes" -lt 2097152 ]; then pass "corpus plus damage is $bytes bytes (under 2 MB)"; else fail "corpus plus damage is $bytes bytes (2 MB limit)"; fi

if [ "$failures" -ne 0 ]; then
  echo "legacy_corpus_test: $failures failure(s)" >&2
  exit 1
fi
echo "legacy_corpus_test: all checks passed"
