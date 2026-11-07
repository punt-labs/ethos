#!/usr/bin/env bash
set -euo pipefail

# Prepare plugin for release: swap name to prod, remove -dev commands.
# The tagged commit has only prod artifacts; the marketplace cache clones
# from it.

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLUGIN_JSON="${REPO_ROOT}/plugin/.claude-plugin/plugin.json"

# Preflight: abort if repo has uncommitted changes
status="$(git -C "$REPO_ROOT" status --porcelain -uno)"
if [[ -n "$status" ]]; then
  echo "Error: repository has uncommitted changes. Commit or stash before running $(basename "$0")." >&2
  exit 1
fi

# Swap plugin name from *-dev to prod
current_name="$(python3 - "$PLUGIN_JSON" <<'PY'
import json
import pathlib
import sys

name = json.loads(pathlib.Path(sys.argv[1]).read_text())["name"]
if not isinstance(name, str) or not name or name == "-dev":
    sys.exit("Error: plugin name must be a nonempty string with a nonempty production name")
print(name)
PY
)"
prod_name="${current_name%-dev}"

if [[ "$current_name" == "$prod_name" ]]; then
  echo "Plugin name is already '${prod_name}' (no -dev suffix)" >&2
  exit 1
fi

echo "Swapping plugin name: ${current_name} → ${prod_name}"
python3 - "$PLUGIN_JSON" "$prod_name" <<'PY'
import json
import pathlib
import sys

p = pathlib.Path(sys.argv[1])
d = json.loads(p.read_text())
d['name'] = sys.argv[2]
p.write_text(json.dumps(d, indent=2) + '\n')
PY

# Remove tracked -dev commands, including nested paths. No commands directory
# (or no dev commands) is legitimate; other git errors must still fail.
git -C "$REPO_ROOT" rm --ignore-unmatch -- ':(glob)plugin/commands/**/*-dev.md'

git -C "$REPO_ROOT" add "$PLUGIN_JSON"
git -C "$REPO_ROOT" commit -m "chore: prepare plugin for release"
