---
name: ethos-collaboration
description: Use Vox for authorized speech, Beadle for email, or Lux for a useful shared visualization
---

# Collaboration adapter

Inspect current tool schemas or installed `vox --help`, `beadle-email --help`
and `lux --help`; use only the surface relevant to the request. Read deposited
tool guidance when needed (including .punt-labs/vox/CLAUDE.md), without editing
its contents or preloading consumer manuals. No Ethos MCP/lifecycle hook is
required to use these independent tools.

Vox: use concise requested speech and respect the user's audio preferences.
Beadle: verify intended account, recipients, subject and content against existing
authorization before sending; a smoke/health check must never send or reply.
Lux: create/update a visualization only when useful and authorized, preserving
the source of truth in code/docs. A connected transport alone proves none of
these operations worked. Use harmless health/list calls for diagnostics and
report unavailable capability precisely. Never expose mail content or secrets
in diagnostics. Agent-authored Ethos/bootstrap evidence belongs under
.punt-labs/local/ethos or .tmp/opencode scratch, not source records. Use each
tool's APIs for its configuration; do not handwrite other tools' state. This
adapter does not relocate Vox, Quarry or Beadle backend state.
