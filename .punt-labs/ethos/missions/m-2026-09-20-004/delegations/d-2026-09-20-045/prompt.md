You are executing ethos mission m-2026-09-20-004 in <repo> on branch feat/z-spec-state-machines (already checked out — do not switch branches).

First run: ethos mission show m-2026-09-20-004 — the contract is authoritative: write_set docs/spec-hook-gates.tex, success criteria, round budget 2, forensic context, inputs.files.

Summary: write a Z specification (fuzz-clean, ProB-checked) of the composed hook gate machine: the SubagentStart verifier gate and the PreToolUse write gate, with the MISSION_ID channel carrying an explicit 'absent' value and ETHOS_VERIFIER_ALLOWLIST as the only input to the write gate. Safety property: the state 'spawn is a verifier of an open mission AND no gate applied AND no diagnostic emitted' (silent-disabled) is unreachable. The current semantics (mute skip at internal/hook/subagent_start.go:599-605) must yield a recorded ProB counter-example; the corrected variant is a LOUD skip — diagnostics only, identical gating decisions, because bead ethos-yf6n is triaged DO NOT FIX AS PROPOSED and the gating behavior itself is by design. Record which of the three empty-MISSION_ID causes (Tier A ad-hoc spawn, PreToolUse mis-dispatch, PreToolUse not installed) are distinguishable at the gate. Follow docs/spec-mission-lifecycle.tex house style and its errata technique.

Tooling: z-spec MCP tools (check = fuzz; test / model_check = ProB); file must be .tex. Run make check before each commit.

Constraints: touch ONLY docs/spec-hook-gates.tex. No Go changes. Commit incrementally on the current branch; do not push. Two other agents are concurrently writing docs/spec-writeset-admission.tex and docs/spec-audit-durability.tex — do not touch those files.

When complete, submit a structured result for round 1 (ethos mission result --help). The result YAML must carry the mission: field naming m-2026-09-20-004; prose must avoid trailing whitespace and YAML-truncating free text. After submitting, stop — evaluator jra and the leader handle review and close.