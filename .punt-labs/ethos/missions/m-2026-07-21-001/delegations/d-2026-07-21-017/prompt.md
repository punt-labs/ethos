You are the pinned evaluator on ethos mission m-2026-07-21-001 (implement archetype). Working directory: /Users/jfreeman/Coding/punt-labs/ethos. Your lens: Go internals, module/dependency structure, compatibility, rolling upgrades — you also evaluated the DES-058 design rounds (.tmp/missions/results/m-2026-07-20-016-eval-rsc.md).

Evaluate the completed implementation on branch feat/des-058-audit-seal-impl (rounds 1+2; ~13 commits off main). The spec is the merged docs/audit-seal.md (authoritative) + the DES-058 DESIGN.md entry. Worker reports: .tmp/missions/results/m-2026-07-21-001-r1-report.md and -r2-report.md; contract: `ethos mission show m-2026-07-21-001`.

Process: read the contract's 12 success criteria and both reports, then review the full branch diff (git diff main...feat/des-058-audit-seal-impl) with the spec open. Run make check yourself; run the tests; exercise the dogfood path yourself with a .tmp/ethos build (do NOT run make install). Judge criterion-by-criterion.

Scrutinize hardest, in order:
1. Spec fidelity on the failure classes — exit 2 (EACCES/EIO/malformed name/corrupt chunk/git-add failure) vs exit 0 (ENOENT+roster warning, nothing-pending, gitlink deferral with stderr notice). Silent-failure regressions here were the design review's biggest findings; verify each class has a test.
2. The new internal/audit leaf package (leader-ratified write-set expansion, import-cycle-driven: mission cannot import hook). Check the dependency direction is genuinely acyclic, the package boundary is coherent (no hook-specific types leaked into audit), and nothing else should have moved with it.
3. Monotonic-ts subsystem: allocation under the flock, torn-tail truncation on reopen (does truncation ever eat a complete line?), last_ts recovery (live tail vs sealed max vs legacy max).
4. Seal correctness: watermark from chunk names + content-verify, temp+rename, orphan git-add across BOTH zones on every seal, stale-temp cleanup under the flock, chunk-name grammar (19-digit zero-pad, parse/classify).
5. Union read: chunk concat + live tail, (session, ts) dedup for post-discipline lines vs byte-equality for legacy, corrupt-chunk error naming the chunk, quarantine gap markers, gitlink deferred-flag; output byte-identical for a single-legacy-file session.
6. Mission-log subsystem parity: per-(mission,session) live log, log chunks, mission-close seal, pre-commit backstop — every audit rule inherited, none forked.
7. Compatibility/upgrade: frozen legacy files never rewritten; a pre-DES-058 tree reads identically; no format breaks in auditEntry JSON (permissive decode preserved).
8. Two known refinements are already queued for round 3 (sealed-dir date from roster; drained legacy missions/<id>.jsonl residue) plus the .gitignore canonical local-zone line — do not re-report those; find what ISN'T known.

Write your review to .tmp/missions/results/m-2026-07-21-001-eval-rsc.md: verdict first (APPROVE / APPROVE WITH NAMED EDITS / ITERATE), numbered findings with severity (REQ/OPT), file:line references, concrete fixes. Then reply exactly "written" — the relay drops long replies; all detail in the file.