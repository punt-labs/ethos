---
name: ethos-opencode-check
description: Diagnose this repository's native OpenCode roster, models, six MCPs or tool execution
---

# OpenCode environment checks

Read docs/opencode-setup.md only for this setup/diagnostic task. Start with
`make check-opencode` (offline). `make doctor-opencode` inspects the live roster,
models/config and six connections; `make smoke-opencode` spends model credits
and launches real children, so only the leader runs it when authorized.
Do not equate catalog/transport presence with completed tool execution.

Preserve evidence under .tmp/opencode, report failures/blocked prerequisites
honestly, and never print raw resolved config, headers or secret environment.
Do not edit .envrc/.envrc.local, install packages, change global settings, delete
sessions or force-remove worktrees to make checks pass. Config hot-reloads;
`/reload` forces refresh. Reconnect an upgraded MCP executable specifically.
Route Qodo through ethos-qodo-review: CLI >=2 keeps the installed skill's gates;
the user-authorized 1.6.0 legacy path is separate. No review-notes endpoint is
required for local review; never claim a failed or blocked review passed.
