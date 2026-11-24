#!/bin/sh
set -eu

fail() {
    printf 'FAIL: ethos %s (%s, %s): %s\n' "$version" "$mode" "$asset" "$*" >&2
    exit 1
}

if [ "$#" -ne 4 ] || { [ "$1" != candidate ] && [ "$1" != published ]; }; then
    printf 'Usage: sh %s <candidate|published> <version> <install.sh> <dist directory>\n' "$0" >&2
    exit 2
fi
mode=$1
version=$2
installer=$(CDPATH='' cd -- "$(dirname -- "$3")" && printf '%s/%s\n' "$(pwd -P)" "$(basename -- "$3")")
dist=$(CDPATH='' cd -- "$4" && pwd -P)
asset="ethos-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m)"
case "$asset" in
    *-x86_64) asset="${asset%-x86_64}-amd64" ;;
    *-aarch64) asset="${asset%-aarch64}-arm64" ;;
esac
case "$asset" in
    ethos-darwin-amd64|ethos-darwin-arm64|ethos-linux-amd64|ethos-linux-arm64) ;;
    *) fail "unsupported release platform" ;;
esac

installer_version=$(sed -n 's/^VERSION="\([^"]*\)"$/\1/p' "$installer")
[ "$installer_version" = "$version" ] || fail "installer must target ethos version $version (found: ${installer_version:-none})"

expected=$(awk -v asset="$asset" '$2 == asset { print $1 }' "$dist/checksums.txt")
case "$expected" in
    ''|*[!0123456789abcdef]*) fail "invalid or duplicate checksum for $asset" ;;
esac
[ "${#expected}" -eq 64 ] || fail "invalid checksum for $asset"

digest() {
    hash=$(shasum -a 256 < "$1") || return 1
    printf '%s\n' "${hash%% *}"
}

python=$(python3 -I -c 'import sys; print(sys.executable)') || fail "python3 is required"
[ -x "$python" ] || fail "could not resolve the Python interpreter"

checkout=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P)
sandbox=$(mktemp -d "${RUNNER_TEMP:-/tmp}/ethos-release-smoke.XXXXXXXX")
trap 'rm -rf -- "$sandbox"' EXIT
trap 'exit 1' HUP INT TERM
sandbox=$(CDPATH='' cd -- "$sandbox" && pwd -P)
case "$sandbox/" in
    "$checkout/"*) fail "smoke must run outside the checkout" ;;
esac
mkdir "$sandbox/home" "$sandbox/work" "$sandbox/tmp"
cd "$sandbox/work"

clean() {
    env -i HOME="$sandbox/home" PATH=/usr/bin:/bin TMPDIR="$sandbox/tmp" \
        GIT_CONFIG_NOSYSTEM=1 GIT_CEILING_DIRECTORIES="$sandbox" PUNT_TELEMETRY=off "$@"
}

set -- sh "$installer" --no-plugin
if [ "$mode" = candidate ]; then
    [ "$(digest "$dist/$asset")" = "$expected" ] || fail "candidate SHA-256 mismatch"
    set -- "ETHOS_LOCAL_BINARY=$dist/$asset" "$@"
fi
if ! clean "$@" > "$sandbox/install.log" 2>&1; then
    cat "$sandbox/install.log" >&2
    fail "installer failed"
fi
if [ "$mode" = published ] && grep -qE 'falling back to source build|building from source' "$sandbox/install.log"; then
    cat "$sandbox/install.log" >&2
    fail "the installer did not use the GitHub Release binary"
fi

ethos="$sandbox/home/.local/bin/ethos"
[ -x "$ethos" ] || fail "the installer did not install an executable"
[ "$(digest "$ethos")" = "$expected" ] || fail "installed SHA-256 differs from the candidate"
if ! clean "$ethos" version --json > version.json ||
    ! clean "$python" -c 'import json, sys; sys.exit(json.load(sys.stdin) != {"version": sys.argv[1]})' "$version" < version.json; then
    cat version.json >&2
    fail "expected ethos version $version"
fi

cat > identity.yaml <<'YAML'
handle: release-smoke
name: Release Smoke
kind: human
YAML
clean "$ethos" identity create -f identity.yaml || fail "identity create failed"
clean "$ethos" identity get release-smoke --json > identity.json || fail "identity get failed"
if ! clean "$python" -c '
import json, sys
identity = json.load(sys.stdin)
expected = {"handle": "release-smoke", "name": "Release Smoke", "kind": "human"}
sys.exit(not all(identity.get(key) == value for key, value in expected.items()))
' < identity.json; then
    cat identity.json >&2
    fail "identity round trip returned unexpected data"
fi
printf 'PASS: identity round trip — ethos %s (%s, %s), SHA-256 %s\n' "$version" "$mode" "$asset" "$expected"
