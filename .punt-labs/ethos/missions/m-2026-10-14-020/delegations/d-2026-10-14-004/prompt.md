You are the worker on ethos mission m-2026-10-14-020. Read it first: `ethos mission show m-2026-10-14-020`. Your write-set is `docs/prfaq-opencode-integration.md` only.

This is Working-Backwards research for bead ethos-5y4r (P1): "First-class native OpenCode 2.x integration for ethos" — adding native OpenCode-to-ethos session binding so mission-owned work performed through OpenCode gets correct commit/PR traceability.

## Required research, with citations

1. **Exemplars.** Find at least 3 real examples of a tool binding identity/audit/traceability state across more than one coding-agent runtime (not necessarily OpenCode-specific — the pattern is "one control-plane tool, multiple agent harnesses"). Use quarry (`mcp__plugin_quarry_quarry__find` or whatever quarry tool you have), context7, and the web. Document what each does and what's transferable.

2. **OpenCode 2.x's actual current session/plugin model — sourced, not assumed.** Use context7:
   - `/websites/opencode_ai_plugins` — plugin lifecycle events: `session.created`, `session.compacted`, `session.deleted`, `session.diff`, `session.error`, `session.idle`, `session.status`, `session.updated`; `tool.execute.before`/`tool.execute.after`.
   - `/anomalyco/opencode` — specifically pull and read in full: `specs/v2/session.md` (describes `SessionExecution` as process-local, keyed by Session ID, with `SessionRunCoordinator` ownership rules: explicit resumes join the active execution, repeated wakes coalesce, different sessions run concurrently, interruption stops locally-owned execution without deleting pending input) and `V2_HTTP_API_AUDIT.md` (the `POST /api/session/{sessionID}/fork` endpoint, which forks history up to an optional `before` message boundary). Also `context.data.session.{list,get,root,family,cost,status,sync,invalidate}` from the CLI plugin docs.
   - If a local shallow clone still exists at `~/Coding/punt-labs/biff/.tmp/opencode-src` (another repo's working tree — read-only reference, do not modify), use it to verify anything context7 is ambiguous about.
   - Read https://opencode.ai/docs/plugins/ and https://opencode.ai/docs/sdk/ directly as well.
   This spec (`specs/v2/session.md`) is itself a close formal exemplar for the kind of invariants ethos needs to define — note explicitly how OpenCode's own model (process-local execution, Session-ID-keyed, explicit fork-to-child with message-boundary semantics) maps or fails to map onto ethos's current session/delegation concepts (you do not need to read ethos internals yourself in depth — that grounding already happened in a parallel spec-writing track; focus your effort on the OpenCode side and the product question).

3. **What "first class" and "native" should concretely mean here — a product decision, not an engineering default.** Options to weigh explicitly: ship as a bundled OpenCode plugin distributed with ethos; a standalone adapter binary/process; config-only wiring (e.g. documented `.opencode/plugins/ethos.ts` the user installs); something else. State a recommendation with reasoning, not just a menu.

4. **Explicit non-goals**, confirm these are respected in your framing: do NOT design Circuit's PR-watch workflow tracking — mutable PR head SHA stays GitHub/Circuit-owned. This work covers only STABLE correlation metadata: OpenCode session ID, Bead ID, ethos mission ID, delegation ID.

5. **Check quarry first** for any prior relevant research — search for "opencode", "cross-harness", "session binding". You will likely find `session-b8a0261f`, a prior research effort about OpenCode's plugin/event model done for a DIFFERENT problem (biff's notification/channel architecture, not ethos traceability) — read it for factual grounding on OpenCode's event model (it's accurate and useful), but do not treat its conclusions as answering THIS product question, they don't.

## Output format

Write `docs/prfaq-opencode-integration.md` as a Working-Backwards PR/FAQ:
- A press release framed at the person who currently LOSES commit/PR traceability when they drive an ethos-owned mission through OpenCode instead of Claude Code (concrete, not abstract — what do they see today, what do they see after).
- FAQs, at minimum: why not just reuse the Claude Code hook mechanism verbatim; what happens if OpenCode forks a session mid-mission (`session.fork`); what happens on ethos service restart; what happens with OpenCode worktree/concurrent sessions; why plugin-vs-adapter-vs-config (your recommendation from point 3).
- Every factual claim about OpenCode's current behavior needs a citation (file/doc + what it says).
- State your confidence level on anything you could not verify directly against source/docs.

Do not write code. Do not touch any file outside `docs/prfaq-opencode-integration.md`. Submit your mission result via `ethos mission result` per this repo's documented schema when done.