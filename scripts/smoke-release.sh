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
require_plugin=${ETHOS_SMOKE_REQUIRE_PLUGIN:-0}
require_plugin_version=${ETHOS_SMOKE_REQUIRE_PLUGIN_VERSION:-0}
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

claude_bin=$(command -v claude 2>/dev/null || true)
node_bin=$(command -v node 2>/dev/null || true)
if [ "$require_plugin" = "1" ] && [ -z "$claude_bin" ]; then
    fail "claude CLI is required when ETHOS_SMOKE_REQUIRE_PLUGIN=1"
fi

checkout=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P)
sandbox=$(mktemp -d "${RUNNER_TEMP:-/tmp}/ethos-release-smoke.XXXXXXXX")
trap 'rm -rf -- "$sandbox"' EXIT
trap 'exit 1' HUP INT TERM
sandbox=$(CDPATH='' cd -- "$sandbox" && pwd -P)
case "$sandbox/" in
    "$checkout/"*) fail "smoke must run outside the checkout" ;;
esac
mkdir "$sandbox/bin" "$sandbox/home" "$sandbox/work" "$sandbox/tmp"
if [ -n "$claude_bin" ]; then
    ln -s "$claude_bin" "$sandbox/bin/claude"
fi
if [ -n "$node_bin" ]; then
    ln -s "$node_bin" "$sandbox/bin/node"
fi
cd "$sandbox/work"

clean() {
    env -i HOME="$sandbox/home" PATH="$sandbox/home/.local/bin:$sandbox/bin:/usr/bin:/bin" TMPDIR="$sandbox/tmp" \
        GIT_CONFIG_NOSYSTEM=1 GIT_CEILING_DIRECTORIES="$sandbox" PUNT_TELEMETRY=off "$@"
}

if [ "$mode" = candidate ]; then
    [ "$(digest "$dist/$asset")" = "$expected" ] || fail "candidate SHA-256 mismatch"
    set -- "ETHOS_LOCAL_BINARY=$dist/$asset" sh "$installer" --no-plugin
elif [ -n "$claude_bin" ]; then
    set -- sh "$installer"
else
    set -- sh "$installer" --no-plugin
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
email: release-smoke@example.invalid
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

plugin_dir=""
plugin_label="CLI-only"
if [ "$mode" = candidate ]; then
    cp -R "$checkout/plugin" "$sandbox/plugin"
    plugin_dir="$sandbox/plugin"
    plugin_label="candidate plugin"
    if [ -n "$claude_bin" ]; then
        if ! clean claude plugin validate "$plugin_dir" < /dev/null > "$sandbox/plugin-validate.log" 2>&1; then
            cat "$sandbox/plugin-validate.log" >&2
            fail "claude plugin validate failed"
        fi
    fi
elif [ -n "$claude_bin" ]; then
    if grep -qiE 'skipping plugin install|plugin install skipped' "$sandbox/install.log"; then
        cat "$sandbox/install.log" >&2
        fail "plugin install was skipped"
    fi
    if ! clean claude plugin list < /dev/null > "$sandbox/plugin-list.txt" 2>&1 ||
        ! grep -Fq 'ethos@punt-labs' "$sandbox/plugin-list.txt"; then
        cat "$sandbox/plugin-list.txt" >&2
        fail "ethos@punt-labs not found in claude plugin list"
    fi
    cache_base="$sandbox/home/.claude/plugins/cache/punt-labs/ethos"
    if [ "$require_plugin_version" = "1" ]; then
        [ -d "$cache_base/$version" ] || fail "expected marketplace plugin version $version"
        plugin_ver="$version"
    elif [ -d "$cache_base/$version" ]; then
        plugin_ver="$version"
    elif [ -d "$cache_base" ]; then
        # shellcheck disable=SC2012 # cache_base holds semver directory names
        plugin_ver=$(ls -1t "$cache_base" 2>/dev/null | head -n 1 || true)
    else
        plugin_ver=""
    fi
    [ -n "$plugin_ver" ] && [ -d "$cache_base/$plugin_ver" ] || fail "marketplace plugin cache directory not found"
    plugin_dir="$cache_base/$plugin_ver"
    plugin_label="marketplace plugin v$plugin_ver"
fi

if [ -n "$plugin_dir" ]; then
    if ! clean "$python" - "$plugin_dir" <<'PY'
import json
import os
import pathlib
import sys

plugin_dir = pathlib.Path(sys.argv[1])
manifest = json.loads((plugin_dir / ".claude-plugin" / "plugin.json").read_text())
self_srv = manifest.get("mcpServers", {}).get("self", {})
if self_srv.get("command") != "ethos" or self_srv.get("args") != ["serve"]:
    sys.exit(f"unexpected mcpServers.self in plugin.json: {self_srv!r}")

hooks_doc = json.loads((plugin_dir / "hooks" / "hooks.json").read_text())
hook_count = 0
for entries in hooks_doc.get("hooks", {}).values():
    for entry in entries:
        for hook in entry.get("hooks", []):
            cmd = hook.get("command", "")
            prefix = "${CLAUDE_PLUGIN_ROOT}/"
            if not cmd.startswith(prefix):
                sys.exit(f"unexpected hook command path: {cmd!r}")
            target = plugin_dir / cmd[len(prefix):]
            if not target.is_file() or not os.access(target, os.X_OK):
                sys.exit(f"missing or non-executable hook script: {target}")
            hook_count += 1
if hook_count == 0:
    sys.exit("hooks.json declared zero hooks")
PY
    then
        fail "plugin manifest or hooks validation failed"
    fi
fi

if ! clean "$python" - "$ethos" <<'PY'
import json
import os
import queue
import subprocess
import sys
import threading
import time

ethos_bin = sys.argv[1]
proc = subprocess.Popen(
    [ethos_bin, "serve"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    env=dict(os.environ),
)
messages: "queue.Queue[bytes]" = queue.Queue()


def reader() -> None:
    assert proc.stdout is not None
    for line in proc.stdout:
        messages.put(line)
    messages.put(b"")


thread = threading.Thread(target=reader, daemon=True)
thread.start()


def send(payload: dict) -> None:
    assert proc.stdin is not None
    proc.stdin.write(json.dumps(payload, separators=(",", ":")).encode() + b"\n")
    proc.stdin.flush()


def call(req_id: int, method: str, params: dict) -> dict:
    send({"jsonrpc": "2.0", "id": req_id, "method": method, "params": params})
    deadline = time.monotonic() + 20
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            sys.exit(f"MCP timeout waiting for {method}")
        try:
            raw = messages.get(timeout=remaining)
        except queue.Empty:
            sys.exit(f"MCP timeout waiting for {method}")
        if not raw:
            stderr = proc.stderr.read().decode(errors="replace") if proc.stderr else ""
            sys.exit(f"MCP server closed before {method} response: {stderr}")
        msg = json.loads(raw)
        if msg.get("id") == req_id:
            if "error" in msg:
                sys.exit(f"MCP {method} error: {msg['error']}")
            return msg["result"]


try:
    call(1, "initialize", {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "clientInfo": {"name": "release-smoke", "version": "1"},
    })
    send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    tools_res = call(2, "tools/list", {})
    names = {t.get("name") for t in tools_res.get("tools", [])}
    required = {"identity", "session", "mission", "doctor"}
    missing = sorted(required - names)
    if missing:
        sys.exit(f"MCP tools/list missing {missing}: {sorted(names)}")
    call_res = call(3, "tools/call", {
        "name": "identity",
        "arguments": {"method": "get", "handle": "release-smoke"},
    })
    if call_res.get("isError"):
        sys.exit(f"MCP identity get returned error: {call_res}")
    text = "\n".join(c.get("text", "") for c in call_res.get("content", []))
    if "release-smoke" not in text or "Release Smoke" not in text:
        sys.exit(f"MCP identity get returned unexpected content: {text!r}")
finally:
    if proc.stdin:
        proc.stdin.close()
    try:
        rc = proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.terminate()
        rc = proc.wait(timeout=5)
    thread.join(timeout=5)
if rc != 0:
    stderr = proc.stderr.read().decode(errors="replace") if proc.stderr else ""
    sys.exit(f"MCP server exited with {rc}: {stderr}")
PY
then
    fail "MCP stdio round trip failed"
fi

if [ -n "$plugin_dir" ]; then
    mkdir -p "$sandbox/work/repo"
    cd "$sandbox/work/repo"
    clean git init -q || fail "git init failed"
    clean git config user.name "Release Smoke" || fail "git config user.name failed"
    clean git config user.email "release-smoke@example.invalid" || fail "git config user.email failed"
    clean "$ethos" enable >/dev/null || fail "ethos enable failed"

    hook_err_log="$sandbox/home/.punt-labs/ethos/hook-errors.log"
    rm -f "$hook_err_log"

    printf '%s\n' '{"session_id":"release-smoke-session"}' |
        clean CLAUDE_PLUGIN_ROOT="$plugin_dir" bash "$plugin_dir/hooks/session-start.sh" >/dev/null ||
        fail "session-start hook failed"
    clean "$ethos" session show release-smoke-session --json > "$sandbox/roster.json" ||
        fail "session show after session-start hook failed"
    if ! clean "$python" -c 'import json, sys; data = json.load(sys.stdin); sys.exit(not data)' < "$sandbox/roster.json"; then
        cat "$sandbox/roster.json" >&2
        fail "session roster returned empty data"
    fi

    printf '%s\n' '{"session_id":"release-smoke-session","tool_name":"Read","tool_input":{"file_path":"README.md"}}' |
        clean CLAUDE_PLUGIN_ROOT="$plugin_dir" bash "$plugin_dir/hooks/pre-tool-use.sh" >/dev/null ||
        fail "pre-tool-use hook failed"
    printf '%s\n' '{"session_id":"release-smoke-session","tool_name":"Bash","tool_input":{"command":"echo release-smoke"}}' |
        clean CLAUDE_PLUGIN_ROOT="$plugin_dir" bash "$plugin_dir/hooks/audit-log.sh" >/dev/null ||
        fail "audit-log hook failed"
    printf '%s\n' '{"session_id":"release-smoke-session","tool_name":"mcp__plugin_ethos_self__whoami","tool_response":"release-smoke"}' |
        clean CLAUDE_PLUGIN_ROOT="$plugin_dir" bash "$plugin_dir/hooks/suppress-output.sh" >/dev/null ||
        fail "suppress-output hook failed"
    printf '%s\n' '{"session_id":"release-smoke-session"}' |
        clean CLAUDE_PLUGIN_ROOT="$plugin_dir" bash "$plugin_dir/hooks/session-end.sh" >/dev/null ||
        fail "session-end hook failed"

    if [ -s "$hook_err_log" ]; then
        cat "$hook_err_log" >&2
        fail "hook-errors.log is non-empty"
    fi
fi

printf 'PASS: CLI + MCP + hooks (%s) — ethos %s (%s, %s), SHA-256 %s\n' "$plugin_label" "$version" "$mode" "$asset" "$expected"
