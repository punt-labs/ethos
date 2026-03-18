#!/usr/bin/env bash
# hooks/session-start.sh — SessionStart hook for ethos plugin
set -euo pipefail

PLUGIN_ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
COMMANDS_DIR="$HOME/.claude/commands"

# Deploy top-level commands (diff-and-copy, not skip-if-exists)
DEPLOYED=()
for cmd_file in "$PLUGIN_ROOT/commands/"*.md; do
  [[ -f "$cmd_file" ]] || continue
  name="$(basename "$cmd_file")"
  [[ "$name" == *-dev.md ]] && continue
  dest="$COMMANDS_DIR/$name"
  mkdir -p "$COMMANDS_DIR"
  if [[ ! -f "$dest" ]] || ! diff -q "$cmd_file" "$dest" >/dev/null 2>&1; then
    cp "$cmd_file" "$dest"
    DEPLOYED+=("/${name%.md}")
  fi
done

# Resolve active identity for context injection
IDENTITY_INFO=""
if command -v ethos >/dev/null 2>&1; then
  IDENTITY_INFO=$(ethos whoami 2>/dev/null || true)
fi

# Build output
OUTPUT=""
if [[ ${#DEPLOYED[@]} -gt 0 ]]; then
  OUTPUT="Ethos: deployed commands: ${DEPLOYED[*]}. "
fi
if [[ -n "$IDENTITY_INFO" ]]; then
  OUTPUT="${OUTPUT}Active identity: ${IDENTITY_INFO}"
fi

if [[ -n "$OUTPUT" ]]; then
  printf '{"hookSpecificOutput":{"additionalContext":"%s"}}' "$OUTPUT"
fi
