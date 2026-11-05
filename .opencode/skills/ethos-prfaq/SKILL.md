---
name: ethos-prfaq
description: Revise or independently review this repository's existing PR/FAQ and its promise-to-acceptance mapping
---

# PR/FAQ adapter

From the current target checkout run `python3 scripts/opencode.py sources prfaq`.
Read the returned maintained SKILL.md and only its relevant references; the
resolver derives siblings from Git common-dir, not this skill's relative path.
If absent, report the exact missing source and stop this workflow.

Use existing prfaq.tex in revise/review mode. Reuse the user's scope, decisions
and supplied evidence; do not repeat answered discovery questions. Read the full
document before targeted edits. Map affected promises to ADRs, implementation
and executable acceptance in both directions. Never weaken a promise just to
describe a defect. Preserve unrelated text and distinguish proposed behavior.

Adapt Claude-specific tool names to native read/patch/shell/subagent tools;
the leader launches separate writer/reviewer contexts when authorized. A
specialist must not launch more agents. Only execute compile/review procedures
within the assigned write-set. Keep intermediates under .tmp/opencode; compile
and inspect actual output when authorized. Read-only discovery means reading
sources, not compiling or changing the PR/FAQ.
