You are the worker on ethos mission m-2026-07-20-013 (design archetype, doc-only). Working directory: /Users/jfreeman/Coding/punt-labs/ethos.

First run: ethos mission show m-2026-07-20-013 — the contract is authoritative for scope, success criteria, and context. Your write set is exactly one file: docs/audit-seal.md.

Task: design the separation of the live audit write path from the committed audit record, amending DES-054 v5 storage. Before writing anything, read:
- DESIGN.md, section "DES-054: Audited delegation" (storage layout, audit-log read-path state machine, concurrency model + I10-audit-atomic invariant, migration section) — your design must amend it coherently, not contradict it.
- docs/audited-delegation.md — especially the path-redaction section (~L485-509) and commit-trailer sections.

The problem, the ruled-out non-solutions, the requested live+seal shape, the invariants to preserve, and the acceptance criteria are all in the mission context — restate them in the doc and satisfy every success criterion in the contract. Decide the two open calls (live-file location: global tree vs gitignored in-repo sibling; mission-tree log.jsonl/.lock treatment) with explicit justification and rejected alternatives. Include proposed DESIGN.md entry text (use the next free DES number — check DESIGN.md for the highest existing one) so the document stage can drop it in verbatim.

Pay particular attention to the seal-step concurrency: appends serialize through the session flock today (per-line f.Sync()); your seal must neither drop nor duplicate lines when a live writer appends mid-seal, and must be safe when git invokes the pre-commit hook from multiple repos/sessions on one machine. Also specify precisely what "repo-wins dedup" means for merged sealed+live reads (key: which fields identify a duplicate line).

Do NOT touch any Go code, install.sh, hooks, or any file other than docs/audit-seal.md. Do not run make install. Use .tmp/ for any scratch.

When the doc is complete, submit your result:
  ethos mission result m-2026-07-20-013 --file <your-result.yaml>
with a YAML body containing: mission, round: 1, author: bwk, verdict (pass/fail/escalate), confidence, files_changed (docs/audit-seal.md with added/removed line counts), evidence (at least one entry, e.g. "success-criteria walkthrough" and "markdownlint docs/audit-seal.md"), and prose summarizing the key decisions. Run npx markdownlint-cli2 docs/audit-seal.md and fix findings before submitting (that is the doc quality gate; full make check compiles Go and is not needed for a doc-only change).

Your final message: a compact summary of the decisions made (live location, seal points, dedup key, mission-tree call, DES number used) and the result submission status.