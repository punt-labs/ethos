You are the worker on round 2 of ethos mission m-2026-07-20-013 (design archetype, doc-only). Working directory: /Users/jfreeman/Coding/punt-labs/ethos. Write set: exactly docs/audit-seal.md. You wrote round 1; this round applies the evaluator's named edits.

First run: ethos mission show m-2026-07-20-013 (contract + round-1 reflection). Then read the evaluator's full review: .tmp/missions/results/m-2026-07-20-013-eval-rsc.md — verdict APPROVE WITH NAMED EDITS, 5 REQ + 5 OPT. Apply every REQ; adopt or explicitly reject each OPT (a rejection goes in Rejected Alternatives with the reason). Keep the doc's structure and the verbatim DES-058 DESIGN.md entry in sync with every change.

The REQ edits:
- R1 — seal resume must not trust size(sealed) as a raw byte offset into live. Anchor resume on content (e.g., the identity of the last sealed line located in live), verify before appending, fail closed with a named remedy on divergence (legacy migrate re-encode, manual edit, merge-conflict resolution, checkout of older sealed state, downgrade skew are the known divergence paths). Update I11-seal accordingly — the invariant must be checked, not just asserted.
- R2 — replace the (session, ts, delegation_id, tool_input_hash) dedup tuple with a per-session monotonic sequence number on every audit line; key identity on (session, seq). This supersedes the ts→RFC3339Nano prerequisite (drop it or demote it to an ordering nicety per O5) and should collapse the merged read to sealed-prefix + live-tail concatenation anchored on seq, with no content dedup. Note R1's content anchor naturally becomes the last sealed seq. Specify the seq field name, where it is allocated (under the session flock), and what happens for legacy lines without seq.
- R3 — audit migrate (temp+rename rewrite of the same global path) and audit seal (append) contend. Fence them: define which verb claims a legacy pre-DES-058 file, and make migrate refuse (or take the session flock and coordinate) on any session the DES-058 live writer manages.
- R4 — lock relocation must remove the repo-tree lock files from DISK and stop writing them, not just git rm --cached (an untracked leftover still fails clean-tree gates). Keep it idempotent and --dry-run-able.
- R5 — state the multi-session pre-commit semantics explicitly. Adopt the evaluator's recommendation: the pre-commit seal covers the committing session plus orphaned sessions (roster PID dead / session ended uncleanly), NOT other currently-live sessions, whose lines stay safely out of tree until their own commits. Define how "orphaned" is determined.

The OPT findings (adopt or reject each, in the doc):
- O1: cut-at-last-'\n' off-by-one vs I11-seal — the sealed range must include the terminator; fix the algorithm text.
- O2: split fail-closed policy — ENOENT/no-live-file is exit 0 no-op; EACCES/EIO is exit 2 block. The remedy message must not depend on the unreadable tree.
- O3: transition section — add the one-time cleanup of already-dirty tracked files at upgrade, and the multi-machine repo skew note (un-upgraded machine keeps dirtying; JSONL merge conflicts possible during the window).
- O4: name the pre-DES-058 reader tail-under-report as a window property, and state the read path is replaced (early-return → merge), not reused.
- O5: only relevant if you keep ts in the identity key; with R2 adopted, demote Nano to ordering hygiene or drop it.

One leader finding, same weight as a REQ:
- L1 — seal repo-scoping must be checkout-path-precise: two checkouts of one repo are two sessions with two live files, and checkout A's pre-commit must never seal checkout B's session into A's tree. State the roster repo-field semantics (path, not repo name) and what happens when a checkout path moves.

Quality gate: npx markdownlint-cli2 docs/audit-seal.md clean. Then submit the round-2 result:
  ethos mission result m-2026-07-20-013 --file <result.yaml>
YAML body: mission, round: 2, author: bwk, verdict, confidence, files_changed (docs/audit-seal.md, added/removed for this round), evidence (markdownlint + a finding-by-finding disposition list: R1-R5, L1, O1-O5 each with applied/rejected), prose with the key mechanism changes. Do not touch any other file; do not run make install.

Final message: disposition per finding (one line each) and the result submission status.