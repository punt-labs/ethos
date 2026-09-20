You are executing ethos mission m-2026-09-20-002 in <repo> on branch feat/z-spec-state-machines (already checked out — do not switch branches).

First run: ethos mission show m-2026-09-20-002 — the contract is authoritative: write_set docs/spec-writeset-admission.tex, success criteria, round budget 2, context with the forensic file:line facts, and inputs.files you must read.

Summary of the work: write a Z specification (fuzz-clean, ProB-checked) of path canonicalization and write-set admission as implemented in internal/mission/conflict.go and consumed by internal/hook/pretooluse.go. Model paths as absoluteness flag + segment sequence; produce decoupled buggy-variant models yielding recorded ProB counter-example traces for ethos-vaib (leading-slash loss) and ethos-8ady (literal glob-string comparison), and a clean corrected-semantics model. Follow the house style and the errata technique of docs/spec-mission-lifecycle.tex exactly — do not bake the safety property into the checked type of the buggy variant.

Tooling: use the z-spec MCP tools (check = fuzz; test / model_check = ProB). The file must have a .tex extension or probcli silently reports 0 states. make check must pass before each commit (your .tex file is not compiled by make check today, so it cannot break it — still run it).

Constraints: touch ONLY docs/spec-writeset-admission.tex plus commits. No Go changes. Commit incrementally on the current branch with clear messages; do not push.

When the spec is complete and fuzz/ProB results are recorded in the document, submit a structured result for round 1: see ethos mission result --help for the exact format. Known format constraints that have cost other agents a round: the result YAML must carry the mission: field naming m-2026-09-20-002, and prose strings must not carry trailing whitespace (they serialize badly) or YAML-truncating free text. After submitting, stop — the evaluator (jra) and leader handle review and close.