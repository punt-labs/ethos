You are the pinned evaluator on mission m-2026-07-31-006 (worker mdm). Evaluate PR #416 in the ethos repo (branch docs/holistic-truing) — a HOLISTIC documentation truing-up of README.md, prfaq.tex, AGENTS.md, DESIGN.md, and docs/**. Your lens: coherence and correctness for a real reader — does the whole corpus now hang together and tell one accurate story about the SHIPPED product?

Get the diff: `git -C <repo> fetch origin && git -C <repo> diff origin/main...origin/docs/holistic-truing`.

Verify, with file:line for anything wrong:
1. ACCURACY vs the built binary — the reconciliations mdm reports (MCP tool count now 12, version v4.8.0, KLOC 44/63, storage path .punt-labs/ethos/, setup asks 5 questions, README badges = License+CI+Go Reference+Working Backwards with NO Release badge). Build .tmp/ethos and spot-check the ones a reader would hit first (README quick-start commands/flags/SHA, the command table, the MCP tool count). Do NOT trust the prose — run it.
2. CROSS-DOC CONSISTENCY — the same command/flag/path/feature/count must read identically across README, AGENTS, docs/**, DESIGN, prfaq. Find any place two docs still disagree.
3. NO LOST CONTENT — this was a truing-up + reorganization, NOT a summarization. Confirm substance was preserved (the README standard forbids stripping; the prfaq thesis + every settled invariant must survive). Flag any place meaning or detail was dropped to look "cleaner".
4. prfaq INTEGRITY — the product thesis and settled invariants (convention-not-enforcement, prospective contracts + close gate, strict decode, human-agent parity, local-only) must be intact and no new positioning invented; it must recompile.
5. README STANDARD conformance (../punt-kit/standards/readme.md) — badges, section order, Title Case, describe-don't-sell tone, no marketing verbs/anthropomorphizing.

EXPECTED, do NOT flag as missing: the #415 mission-lifecycle-cluster DESIGN.md ADR is deliberately absent — that PR is unmerged, so documenting it here would describe unshipped behavior. mdm escalated this correctly.

Deliver PASS or a specific must-fix list (file:line + why it misleads a reader). Do not fix anything. Report via your return value.