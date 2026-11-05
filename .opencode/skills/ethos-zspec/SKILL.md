---
name: ethos-zspec
description: Model or review tricky Ethos state transitions in Z using maintained z-spec procedures
---

# Z-spec adapter

Run `python3 scripts/opencode.py sources zspec` from the current checkout.
Read the relevant returned command/reference, starting with code2model/check
for Z modeling and type checking. Discover current installed contracts with
`z-spec --help` and `z-spec doctor`; do not assume bare PATH finds ProB.
Report exact missing sources or failed prerequisites, not a successful skip.

Read task PR/FAQ/ADR/spec clauses and implementation first. Define state,
initialization, invariants, preconditions, error outcomes and refinement.
Preserve explicit finite bounds for animation. Distinguish fuzz type checking,
ProB bounded model checking, deadlock detection and proof; record actual counts,
counterexamples, commands and exit results.

Use native zspec MCP tools only after inspecting their current schemas. Some
write reports beside the input: copy inputs into .tmp/opencode and direct
outputs there; never mutate source reports during an availability check.
CLI fallback uses installed help, not guessed flags. No automatic setup/install,
source formatting or nested delegation. Writer jms and reviewer jra are separate
contexts launched by the leader. Reuse known decisions instead of rediscovery.
