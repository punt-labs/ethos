Apply the scoped-MCP grant to the shared team registry — the org-wide counterpart to ethos PR #424 (which only touched the ethos repo's own native roles). This is a punt-labs/team change.

The team repo is the submodule in the workspace: ~/Coding/punt-labs/.punt-labs/ethos/ (git remote should be github.com/punt-labs/team). CONFIRM that first (git -C there `config --get remote.origin.url`). It holds ~11 role files; the 9 specialist roles are everything EXCEPT ceo.yaml and coo.yaml (leadership keeps outbound). bwk found these earlier: cli-specialist, finance-ops, go-specialist, infra-engineer, pm-building-blocks, pm-grounding, python-specialist, security-engineer, ux-designer — verify the actual set by listing the roles dir.

To each specialist role's `tools:` list, add exactly this INBOUND set (explicit full names, NOT a wildcard):
- mcp__plugin_quarry_quarry__find, __remember, __show, __ingest, __use, __status, __list
- mcp__plugin_biff_tty__plan, mcp__plugin_biff_tty__read_messages
- mcp__plugin_ethos_self__identity, mcp__plugin_ethos_self__session
Do NOT add z-spec — the team repo has no formal-methods roles (z/b-specialist exist only in ethos).

HARD BOUNDARY (djb will verify): no outbound/side-effecting tool in any specialist role — no biff write/wall/talk, no beadle, no github, no mcp__plugin_ethos_self__mission, no lux. The leader-only surface stays off the list.

Match the exact YAML style already used in ethos's #424 edits (same tool names, same list format) for consistency. Branch in the team submodule, commit to punt-labs/team, open a PR against punt-labs/team (branch protection applies — no direct push to main). Do NOT merge — djb reviews the boundary and I gate. Report the PR number, the roles edited, and confirm via grep that no outbound tool appears in any specialist role. GH_TOKEN valid (claude-puntlabs), plain gh, no env -u. If you hit anything unexpected about the repo topology, stop and tell me before writing.