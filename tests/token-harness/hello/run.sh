#!/usr/bin/env bash
# L6 hello-world: start LiteLLM proxy, invoke `claude --print` against it,
# verify capture, tear down. Exits 0 on success, 1 on any failure.
#
# Prereqs: `pip install 'litellm[proxy]==1.81.9'` (or newer once
# LiteLLM's proxy import against latest FastAPI is fixed) and
# `claude` CLI on PATH. Both work in CI.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$HERE" rev-parse --show-toplevel)"

# All ephemeral artifacts land under .tmp/ (gitignored).
export TOKEN_CAPTURE_DIR="$REPO_ROOT/.tmp/token-captures/hello"
LOG_DIR="$REPO_ROOT/.tmp/token-harness/hello"
LOG_FILE="$LOG_DIR/litellm.log"
PID_FILE="$LOG_DIR/litellm.pid"

mkdir -p "$TOKEN_CAPTURE_DIR" "$LOG_DIR"
rm -f "$TOKEN_CAPTURE_DIR"/*.jsonl "$LOG_FILE" "$PID_FILE"

# Pick an ephemeral port so we don't collide with anything on the box.
PORT="${TOKEN_HARNESS_PORT:-34117}"

echo "[hello] starting LiteLLM proxy on 127.0.0.1:$PORT"
(
  cd "$HERE"
  PYTHONPATH=. litellm --config litellm.yaml --port "$PORT" --host 127.0.0.1 \
    > "$LOG_FILE" 2>&1 &
  echo $! > "$PID_FILE"
)

# Cleanup on exit, always.
cleanup() {
  local rc=$?
  if [ -f "$PID_FILE" ]; then
    kill "$(cat "$PID_FILE")" 2>/dev/null || true
    rm -f "$PID_FILE"
  fi
  exit "$rc"
}
trap cleanup EXIT

# Wait for the proxy to be listening. ~10s max.
for i in $(seq 1 20); do
  if python3 -c "
import socket, sys
try:
    with socket.create_connection(('127.0.0.1', $PORT), timeout=0.5):
        sys.exit(0)
except OSError:
    sys.exit(1)
" 2>/dev/null; then
    echo "[hello] proxy listening after ${i}x 0.5s"
    break
  fi
  sleep 0.5
  if [ "$i" -eq 20 ]; then
    echo "[hello] proxy failed to start after 10s; log tail:" >&2
    tail -30 "$LOG_FILE" >&2
    exit 1
  fi
done

echo "[hello] invoking claude --print against the proxy"
# Hermetic-ish: --bare skips hooks, plugins, MCP, CLAUDE.md.
# --max-turns 1 caps the loop; --print is non-interactive.
# The env cage isolates from the ambient session's Claude Code config.
env -i \
  HOME="$HOME" \
  PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:$HOME/.local/bin" \
  ANTHROPIC_BASE_URL="http://127.0.0.1:$PORT" \
  ANTHROPIC_AUTH_TOKEN="sk-litellm-test-hello-world" \
  ANTHROPIC_MODEL="mock-anthropic" \
  claude --print --output-format json --max-turns 1 "reply with the single word pong" \
  > "$LOG_DIR/claude.json" 2>&1 || true

# Verify: at least one capture file with a proxy_server_request body.
CAPTURE_COUNT=$(ls -1 "$TOKEN_CAPTURE_DIR"/*.jsonl 2>/dev/null | wc -l | tr -d ' ')
if [ "$CAPTURE_COUNT" -eq 0 ]; then
  echo "[hello] FAIL: zero capture files in $TOKEN_CAPTURE_DIR" >&2
  echo "[hello] proxy log tail:" >&2
  tail -30 "$LOG_FILE" >&2
  echo "[hello] claude output tail:" >&2
  tail -20 "$LOG_DIR/claude.json" >&2
  exit 1
fi

# Concrete pass/fail assertions on the captured payload. See the
# README's "Assertions" section for the reasoning behind each threshold.
# The size ratchet and PII patterns are what makes this a real CI test,
# not just an integration smoke check.
python3 - <<'PY'
import glob, json, os, re, sys

captures = sorted(glob.glob(os.path.join(os.environ["TOKEN_CAPTURE_DIR"], "*.jsonl")))
if not captures:
    print("[hello] FAIL: no captures", file=sys.stderr)
    sys.exit(1)

first = json.loads(open(captures[0]).read())
psr = first.get("proxy_server_request", {})
body = psr.get("body", {})

failures = []

# --- Structural assertions -------------------------------------------
required = ["model", "messages"]
for k in required:
    if k not in body:
        failures.append(f"structural: capture body missing key {k!r}")

# --- Payload-size ratchet --------------------------------------------
# Current baseline captured on 2026-08-15 (Claude Code 2.1.220, bare
# invocation, no ethos content) was ~437 KB, 135 tool schemas. If the
# base client footprint or our locally-installed MCP surface changes
# such that the payload crosses 700 KB, we want to see it in CI. Lower
# the ceiling as we drive it down; do NOT quietly raise it.
MAX_TOTAL_BYTES = 700 * 1024
total_bytes = os.path.getsize(captures[0])
if total_bytes > MAX_TOTAL_BYTES:
    failures.append(
        f"size: capture is {total_bytes:,} bytes; ceiling is "
        f"{MAX_TOTAL_BYTES:,} — payload grew past the ratchet"
    )

# --- PII / secret-leak absence ---------------------------------------
# Nothing that looks like a credential should ever ride in a Claude
# Code payload. A hit here means either the mock scenario itself is
# tainted (fix the scenario) or the client leaked something from the
# environment into the prompt (much worse — Phase 6's attribution
# parser will point at the source). Patterns are conservative on
# purpose: false positives are cheaper than false negatives.
raw = open(captures[0]).read()
SECRET_PATTERNS = [
    (r"sk-live-[A-Za-z0-9]{16,}",              "Anthropic/OpenAI live key"),
    (r"sk-ant-api03-[A-Za-z0-9_\-]{20,}",       "Anthropic API key"),
    (r"AKIA[0-9A-Z]{16}",                       "AWS access key"),
    (r"AIza[0-9A-Za-z_\-]{35}",                 "Google API key"),
    (r"ghp_[A-Za-z0-9]{36,}",                   "GitHub personal token"),
    (r"ghs_[A-Za-z0-9]{36,}",                   "GitHub server-to-server token"),
    (r"github_pat_[A-Za-z0-9_]{80,}",           "GitHub fine-grained PAT"),
    (r"-----BEGIN [A-Z ]*PRIVATE KEY-----",     "PEM private key block"),
    (r"xox[baprs]-[A-Za-z0-9\-]{10,}",          "Slack token"),
]
for pattern, label in SECRET_PATTERNS:
    if re.search(pattern, raw):
        failures.append(f"pii: capture contains what looks like a {label}")

# --- Report ----------------------------------------------------------
sys_msgs = body.get("system") or []
tools = body.get("tools") or []
messages = body.get("messages") or []
sys_chars = sum(len(s.get("text", "")) for s in sys_msgs if isinstance(s, dict))
tools_chars = len(json.dumps(tools))
messages_chars = len(json.dumps(messages))
total = total_bytes

if failures:
    print(f"[hello] FAIL: {len(failures)} assertion(s) failed:", file=sys.stderr)
    for f in failures:
        print(f"[hello]   - {f}", file=sys.stderr)
    print(f"[hello] first capture: {captures[0]}", file=sys.stderr)
    sys.exit(1)

print(f"[hello] OK: {len(captures)} capture(s), first={captures[0]}")
print(f"[hello] assertions passed:")
print(f"[hello]   structural: body has {required}")
print(f"[hello]   size:       {total:,} bytes <= {MAX_TOTAL_BYTES:,} ceiling")
print(f"[hello]   pii:        zero secret-shaped patterns found ({len(SECRET_PATTERNS)} checked)")
print(f"[hello] attribution preview:")
print(f"[hello]   total capture:  {total:>7,} bytes")
print(f"[hello]   system prompt:  {sys_chars:>7,} ({sys_chars*100//max(total,1)}%)")
print(f"[hello]   tool schemas:   {tools_chars:>7,} ({tools_chars*100//max(total,1)}%) — {len(tools)} tools")
print(f"[hello]   messages:       {messages_chars:>7,} ({messages_chars*100//max(total,1)}%) — {len(messages)} turns")
PY

echo "[hello] pass"
