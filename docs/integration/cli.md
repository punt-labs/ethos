# CLI Integration

Call ethos commands from hooks, scripts, and CI pipelines. Requires
the `ethos` binary on PATH.

## When to Use

Your tool runs shell commands and wants structured identity data.
The CLI returns JSON with `--json` for machine parsing or plain text
for human reading.

## Check if Ethos is Installed

```bash
if command -v ethos >/dev/null 2>&1; then
  # ethos is available
fi
```

## Read the Active Identity

```bash
# Plain text
ethos whoami

# JSON — parse with jq
ethos whoami --json | jq -r '.handle'
ethos whoami --json | jq -r '.name'
ethos whoami --json | jq -r '.writing_style'
```

## Read a Specific Identity

```bash
# Full identity with resolved attribute content
ethos identity get claude --json

# Reference only (slugs, not content)
ethos identity get claude --reference --json
```

## Read Team Context

```bash
# Which team covers this repo?
ethos team for-repo punt-labs/ethos --json | jq -r '.name'

# Show team members and roles
ethos team show engineering --json | jq '.members[] | {identity, role}'
```

## Read Role Restrictions

```bash
# What tools can this role use?
ethos role show go-specialist --json | jq '.tools'

# What are the safety constraints?
ethos role show go-specialist --json | jq '.safety_constraints'
```

## Read Mission State

```bash
# List open missions
ethos mission list --status open --json

# Show a specific mission's contract
ethos mission show m-2026-04-10-003 --json

# Read the audit trail
ethos mission log m-2026-04-10-003 --json
```

## Read ADRs

```bash
# List all ADRs
ethos adr list --json

# Show a specific ADR
ethos adr show DES-041 --json
```

## Use in Hooks

A Claude Code PostToolUse hook that reads identity:

```bash
#!/usr/bin/env bash
# hooks/greet.sh — example identity-aware hook
command -v ethos >/dev/null 2>&1 || exit 0

NAME=$(ethos whoami --json 2>/dev/null | jq -r '.name // empty')
if [[ -n "$NAME" ]]; then
  echo "Hook running for $NAME"
fi
```

## Use in CI

```yaml
# .github/workflows/check-identity.yml
- name: Check ethos identity
  run: |
    if command -v ethos >/dev/null 2>&1; then
      ethos doctor --json
      ethos identity list --json
    else
      echo "ethos not installed — skipping identity checks"
    fi
```

## Degradation

```bash
get_user_name() {
  if command -v ethos >/dev/null 2>&1; then
    ethos whoami --json 2>/dev/null | jq -r '.name // empty'
  fi
  # Fall back
  git config user.name 2>/dev/null || echo "${USER:-unknown}"
}
```
