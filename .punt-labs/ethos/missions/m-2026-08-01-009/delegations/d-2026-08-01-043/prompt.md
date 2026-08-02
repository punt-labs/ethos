You have mission m-2026-08-01-009 in the ethos repo. Read it first: `ethos mission show m-2026-08-01-009`.

Context (settled in design): specialist sub-agents get their tools from their ROLE's `tools:` list — the generator (internal/hook/generate_agents.go) loads `roles.Load(m.Role)` and writes `r.Tools` verbatim into `.claude/agents/<handle>.md`. A probe confirmed: MCP access is GATED by that `tools:` list (a scoped specialist has zero MCP tools). So adding `mcp__…` names to a role grants them, scoped and enforceable. This grants the dispatched specialists a scoped INBOUND MCP set while keeping outbound leader-only.

The roles live in the team submodule: `<repo>/.punt-labs/ethos/roles/*.yaml`, which is the `punt-labs/team` repo. Work there, branch in that submodule, commit to punt-labs/team, and open the PR against punt-labs/team (branch protection applies — no direct push to main).

THE CHANGE — edit every role file EXCEPT ceo.yaml and coo.yaml (leadership keeps outbound). To each specialist role's `tools:` list, add:
- quarry: mcp__plugin_quarry_quarry__find, __remember, __show, __ingest, __use, __status, __list (memory + search; omit the destructive/admin ones — delete, register_directory, deregister_directory, sync_all_registrations)
- biff: mcp__plugin_biff_tty__plan, mcp__plugin_biff_tty__read_messages ONLY
- ethos: mcp__plugin_ethos_self__identity, mcp__plugin_ethos_self__session ONLY
ADDITIONALLY, to the formal-methods roles ONLY (z-specialist.yaml, b-specialist.yaml): mcp__plugin_z-spec_zspec__check, __model_check, __test, __animate, __browse, __get_report.

Use explicit full `mcp__<server>__<tool>` names. If you empirically confirm the `mcp__<server>__*` wildcard is honored in a sub-agent `tools:` list, you may collapse to it; otherwise keep explicit names.

HARD BOUNDARY — this is the security property djb will check: do NOT add ANY outbound/side-effecting tool to a specialist role. No biff write/wall/talk, no beadle (email) tools, no github tools, no `mcp__plugin_ethos_self__mission` (mission dispatch is leader-only), no lux. Enforce the boundary purely by which names appear.

VERIFY structurally (the live demo is restart-gated — post-merge, don't block on it): build a binary at .tmp/ethos (do NOT `make install`), regenerate `.claude/agents/` from the edited team data (via the binary / the generate-agents path), and confirm (a) a specialist agent file (e.g. bwk's) now lists the granted MCP tools and (b) NO specialist agent file lists any outbound tool. Paste the evidence.

Commit incrementally in the submodule; each commit sane. Open the punt-labs/team PR; report the PR number, the list of roles edited, and your structural verification. Submit the m-2026-08-01-009 result. Do NOT merge — djb reviews the boundary and I gate. GH_TOKEN valid (claude-puntlabs), plain gh, no env -u. If a fix needs a file outside .punt-labs/ethos/roles, escalate to me to re-scope.