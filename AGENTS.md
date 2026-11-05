# Developing Ethos

Ethos is a Go CLI, MCP server, and harness integration for identity, delegation,
and code provenance. OpenCode V2 is the primary harness for this work.

## Authority and context

This file governs the development workflow. OpenCode also loads
`docs/opencode-workflow.md` through `opencode.json`. For this authorized repair,
the optional-orchestration policy below takes precedence over legacy mandatory
Ethos pipeline and Claude-specific dispatch instructions in imported guides.
The applicable engineering standards and independent review requirements remain.
Read the relevant sections of `prfaq.tex`, `DESIGN.md`, and the affected
specification before proposing or implementing a change. Verify design claims
against code: an ADR's settled status does not prove implementation is complete.

- Product vision: `prfaq.tex`; keep the product and its promises aligned.
- Architecture and amendments: `DESIGN.md`.
- Engineering reference and quality gates: `docs/development.md` and `Makefile`.
- Native workflow: `docs/opencode-workflow.md`.
- One-time harness setup: `docs/opencode-setup.md` (read when needed).
- Consumer CLI/MCP reference: `docs/agent-guide.md` (read when needed).

Follow the applicable standards checklist in `docs/development.md`. Changes to
mission-lifecycle states or guards must update `docs/spec-mission-lifecycle.tex`
in the same PR, or explain in the PR why the model is unaffected.

## Storage and product invariants

For repository-owned Ethos data, intended Git records belong in
`.punt-labs/ethos/`; everything intended to be ignored belongs in
`.punt-labs/local/ethos/`. Fix a writer's destination instead of adding ignore
exceptions inside the record tree. Configuration and harness integration files
have their documented locations, including `.punt-labs/ethos.yaml`,
`opencode.json`, `.opencode/`, and `.claude/agents/`.

Preserve blame through squash merges and fresh clones, mandatory mission round
decisions, and usable local audit history. Shared store roots and checkout roots
are different in linked worktrees; use the established resolver. Preserve
identity/extension semantics, redaction, atomic writes, and lock ordering.
The approved storage repair is still work to implement, not a current guarantee.

## Working method

The primary agent owns scope, integration, Git/GitHub operations, and final
verification. Use native specialists for bounded implementation, formal/product
work, and independent review. Give each an explicit task, write set, governing
references, and observable acceptance criteria. Check results against the diff
and actual tool evidence. Workers and reviewers use separate contexts.

Track substantive work in Beads (`bd ... --json`). For the authorized storage
repair, Ethos missions are optional: the product under repair must not prevent
its own repair. Native OpenCode agents do not imply Claude hook parity or the
proposed DES-078 adapter. Circuit is not a prerequisite.

Preserve unrelated changes and active worktrees. Never stage `.envrc`, local
credentials, or Vox state as incidental work. Commit intended paths explicitly.
Use `.tmp/opencode/` for disposable checks and reports. Use
`.punt-labs/local/ethos/` for persistent machine-local Ethos data.

## Verification and delivery

- `make check`: the required repository quality gate.
- `make check-opencode`: offline harness configuration and helper tests.
- `make doctor-opencode`: live workstation and connection checks.
- `make smoke-opencode`: actual specialist/tool execution checks.
- `make docs-pdf`: compile changed product/formal documents when applicable.

Verify through the real changed entry point. A model listed in a catalog, a
connected transport, or a test suite that exercises nothing is not a pass.
Record the exact tested commit, command, result, and any remaining uncertainty.
Use Qodo and independent review before PR handoff. Address findings and verify
the latest head before squash-merging; retain attribution through that merge.

For the storage effort, deliver in order: harness setup; product and PR/FAQ
alignment plus implementation; verification; the actual punt-kit release
playbook; Ethos repository cleanup; consuming-repository rollout.
