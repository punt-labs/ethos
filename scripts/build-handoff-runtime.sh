#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || ! -d $1 || $(uname -s)/$(uname -m) != Linux/x86_64 ]]; then
    printf 'Usage on Linux amd64: %s OUTPUT_DIRECTORY\n' "$0" >&2
    exit 2
fi

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
output=$(cd -- "$1" && pwd -P)
work=$(mktemp -d "$output/.ethos-runtime.XXXXXX")
trap 'chmod -R u+w -- "$work"; rm -rf -- "$work"' EXIT
runtime=$work/runtime
mkdir -p "$runtime/bin" "$runtime/cache/"{go-mod,npm,uv} "$runtime/licenses" \
    "$runtime/node/"{bin,lib/node_modules} "$work/"{home,tmp}

go_tool=$(command -v go)
toolchain=$(awk '$1 == "toolchain" {print $2}' "$repo/go.mod")
test -n "$toolchain"
go_home=$(GOTOOLCHAIN=$toolchain GOENV=off "$go_tool" env GOROOT)
cp -R -P "$go_home" "$runtime/go"
test "$(GOTOOLCHAIN=local "$runtime/go/bin/go" env GOVERSION)" = "$toolchain"

uv_tool=$(command -v uv)
node_tool=$(node -p 'process.execPath')
node_home=$(dirname -- "$(dirname -- "$node_tool")")
shellcheck_tool=$(realpath "$(command -v shellcheck)")
fuzz_tool=$(realpath "$(command -v fuzz)")
fuzz_lib=${FUZZLIB:-$(dirname -- "$fuzz_tool")/../lib/fuzzlib}
install -m 0755 "$uv_tool" "$shellcheck_tool" "$fuzz_tool" "$runtime/bin/"
install -m 0644 "$fuzz_lib" "$runtime/fuzzlib"
install -m 0755 "$node_tool" "$runtime/node/bin/node"
cp -R -P "$node_home/lib/node_modules/npm" "$runtime/node/lib/node_modules/"
ln -s ../lib/node_modules/npm/bin/npm-cli.js "$runtime/node/bin/npm"
ln -s ../lib/node_modules/npm/bin/npx-cli.js "$runtime/node/bin/npx"
install -m 0644 "$node_home/LICENSE" "$runtime/licenses/node-LICENSE"
install -m 0644 "$(dirname -- "$shellcheck_tool")/LICENSE.txt" "$runtime/licenses/shellcheck-LICENSE.txt"
uv_version=$("$uv_tool" --version | awk '{print $2}')
for notice in LICENSE-APACHE LICENSE-MIT; do
    curl --proto '=https' --proto-redir '=https' --fail --silent --show-error --location \
        --connect-timeout 15 --max-time 180 --output "$runtime/licenses/uv-$notice" \
        "https://raw.githubusercontent.com/astral-sh/uv/$uv_version/$notice"
done

isolated=(env -i "HOME=$work/home" "TMPDIR=$work/tmp" LC_ALL=C TZ=UTC
    "PATH=$runtime/go/bin:$runtime/bin:$runtime/node/bin:/usr/bin:/bin"
    GOENV=off GOTOOLCHAIN=local "GOMODCACHE=$runtime/cache/go-mod" "GOBIN=$runtime/bin")
"${isolated[@]}" make -C "$repo" tools >&2
golangci_version=$(awk '$1 == "GOLANGCI_LINT_VERSION" {print $3}' "$repo/Makefile")
curl --proto '=https' --proto-redir '=https' --fail --silent --show-error --location \
    --connect-timeout 15 --max-time 180 --output "$runtime/licenses/golangci-lint-LICENSE" \
    "https://raw.githubusercontent.com/golangci/golangci-lint/$golangci_version/LICENSE"

# Download from temporary manifests so Go cannot rewrite a source go.sum.
mkdir "$work/current"
cp "$repo/go.mod" "$repo/go.sum" "$work/current/"
"${isolated[@]}" go -C "$work/current" mod download all
base=$(git -C "$repo" merge-base HEAD refs/remotes/origin/main)
for rev in "$base" 93c9f82 v4.8.0; do
    mkdir -p "$work/modules/$rev"
    git -C "$repo" archive "$rev" go.mod go.sum | tar -x -C "$work/modules/$rev"
    "${isolated[@]}" go -C "$work/modules/$rev" mod download all
done

python_version=3.13.18
python_env=("${isolated[@]}" "UV_PYTHON_INSTALL_DIR=$work/python" "UV_CACHE_DIR=$work/python-cache")
"${python_env[@]}" uv --no-config python install "$python_version" --no-bin >&2
python_tool=$("${python_env[@]}" uv --no-config python find --managed-python --no-project --system "$python_version")
python_home=$("${isolated[@]}" "$python_tool" -I -c 'import sys; print(sys.base_prefix)')
mv -- "$python_home" "$runtime/python"
python_tool=$runtime/python/bin/python3.13
"${isolated[@]}" "UV_PYTHON=$python_tool" UV_PYTHON_DOWNLOADS=never \
    "UV_CACHE_DIR=$runtime/cache/uv" "UV_PROJECT_ENVIRONMENT=$work/venv" UV_LINK_MODE=copy \
    uv --no-config sync --project "$repo/tests/e2e" --locked >&2

"${isolated[@]}" "npm_config_cache=$runtime/cache/npm" npm_config_ignore_scripts=true \
    npm_config_audit=false npm_config_fund=false npm_config_update_notifier=false \
    npx --yes --package=markdownlint-cli2 -- node --version >&2
rm -rf -- "$runtime/cache/npm/_logs" "$runtime/cache/go-mod/cache/vcs"
find "$runtime/cache/go-mod/cache/download" -name '*.lock' -type f -delete

# Normalize tar metadata. Package-manager cache bytes can differ across
# builds, so the release asset, not a fresh local rebuild, defines the pin.
LC_ALL=C tar --sort=name --format=gnu --mtime=@0 --numeric-owner \
    --owner=0 --group=0 --hard-dereference -C "$runtime" -cf - \
    bin cache fuzzlib go licenses node python | gzip -n > "$work/runtime.tar.gz"
sha=$(sha256sum "$work/runtime.tar.gz")
archive=$output/${sha%% *}.tar.gz
mv -- "$work/runtime.tar.gz" "$archive"
printf '%s\n' "$archive"
