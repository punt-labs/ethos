Audit the ethos open-bead backlog against git history and the current code, in /Users/jfreeman/Coding/punt-labs/ethos, to find beads that are ALREADY FIXED but still open (stale). READ-ONLY — do not edit code, do not close beads, do not touch git. Just produce a verdict table; the leader closes them.

For EACH of these 42 open bead IDs, run `bd show <id>` to get the full description, then determine whether it's already resolved:
- Check git history: `git log --oneline --all | grep -iE '<keywords>'` and `git log --all --grep=<id>` (some fix commits cite the bead ID directly, like #368/#370 did).
- Check the code: grep/read the relevant files to see if the described behavior/fix is already present.

Bead IDs (all P1-P4, current backlog):
n4tk 548q 7t5z 7tqd 7vo3 8bp bljl buyo cmmu d17 e05k ersr fn0m fwzp g2f ggtu hb42 hnxz kyfx lug n43b n4np ni0y qy7k t2lb u4kq wb4 14r7 2kk 4pvt 5yej be44 cm2r cpi.6 dzcy gn7o gu3p hy40 k2xs kcbv tksw ty7f y9t

Classify each into exactly one:
- STALE-FIXED — the fix is demonstrably in a merged commit or the current code. Cite the evidence (commit SHA/subject OR file:line). HIGH BAR: only if you can point to concrete proof the described defect no longer exists.
- OPEN — no evidence of a fix; genuinely outstanding.
- UNCERTAIN — partial/ambiguous; say what you couldn't confirm and what would settle it.

Known-fixed context (do NOT re-audit, just background): 2q0n, yofr, jqpe, kpa0, q6af were already verified fixed + closed this session. The golangci-lint adoption (#402/#404) shipped; the entity schema command (#400, DES-066), manifest-aware seed (#396, DES-065), CEO/COO default (#390), fresh-install WARN (#388), --no-plugin (#377) all merged.

Watch for likely-stale ones: n43b (punt/homebrew — note: punt still does NOT auto-propagate homebrew, so likely still OPEN), cmmu (staticcheck fallback — may be moot now golangci-lint replaced staticcheck in make check), gn7o (shellQuote unit test — adb fixed the comments but did a dedicated test land?), ersr/n4np (delegation prompt redaction — check if write-time redaction exists), t2lb/qy7k (mission pipeline/glob bugs).

Write the verdict table to .tmp/bead-audit.md — columns: ID | Priority | Verdict | Evidence (commit or file:line) | one-line note. Group by verdict (STALE-FIXED first). End with a count summary. Reply "written — N stale, M open, K uncertain".