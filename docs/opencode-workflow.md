# Native OpenCode workflow

## Authority and scope

Read [development standards](development.md) for architecture and gates.
Read relevant portions of `prfaq.tex`, `DESIGN.md` (ADRs), and `docs/spec-*.tex`
before changing their contract. The consumer guide at `docs/agent-guide.md`
is reference material, not a development workflow.

For the explicitly authorized storage-boundary repair and bootstrap
(`ethos-t1uy`, DES-079 execution plan), native OpenCode task delegation replaces
the mandatory Ethos pipeline/mission choreography in the development guide.
Do not create missions merely to repair Ethos. This exception does not waive
review, product promises, acceptance evidence, or release standards. Elsewhere,
follow the task's authorization; do not infer permission to publish or install.
This is harness configuration, not DES-078 lifecycle/audit-hook parity.

## Bounded work and independent review

The build leader owns scope, architecture, integration, Git and publication.
Assign a task ID, round, explicit write-set, success criteria, relevant
PR/FAQ/ADR/spec references, and required checks with each specialist request.
Use a fresh independent reviewer context, never the writer as its evaluator.
Specialists must not delegate further. Permissions and task write-sets are not
a shell sandbox: tests execute repository code with the operator's authority.
Inspect commands and scripts before execution; do not use shell to bypass
environment-file protection or write-set limits.

Use `bwk` for implementation, `rsc` for independent Go review, `djb` for
security, `jms` for modeling, `jra` for independent formal review, `ghr` for
product review, `qa` for executable acceptance, and `researcher` for bounded
read-only research. The three additional review perspectives are
`code-reviewer`, `silent-failure-hunter`, and `invariant-completeness-reviewer`.

Each specialist's final answer begins with this exact header (substitute values):

```text
--- task-result ---
task: ethos-t1uy
round: 1
author: bwk
--- end-task-result ---
```

Follow with changes/findings, actual tested commands and exit results, evidence
paths, and remaining limitations. Do not claim completion from intent, tool
catalog presence, a skipped test, or a connected MCP transport alone.
The parent obtains the exact child ID from native subagent metadata/session API;
never guess IDs or select the last message by array order. Use
`python3 scripts/opencode.py extract CHILD PARENT TASK ROUND AUTHOR OUTPUT`
to correlate identity and the latest completed result. Output must be under
`.tmp/opencode/` (scratch) or `.punt-labs/local/ethos/` (persistent local state).

## Review and delivery

Run applicable focused checks during implementation and `make check` before
handoff. Route local Qodo review through `ethos-qodo-review`, plus all three
seeded review perspectives before opening a PR. CLI >=2 delegates to the
installed `qodo-review` skill with its runtime/API/authentication gates intact.
The user explicitly authorized the restored checksum-verified 1.6.0 CLI's legacy
review contract after 2.1 A2A failures for both the ethos-t1uy bootstrap and the
approved DES-079 storage-boundary repair, including after bootstrap closes, until
a separately verified, user-directed upgrade. This is not a pass of the newer
installed skill. Do not edit global skills or silently upgrade.
Use actual merge-base, current context and full intended final-review scope;
resolve actionable findings using independent evidence. See the adapter for
the narrow version route, bounded recovery and truthful coverage reporting.

PR policy remains punt-kit `standards/pr-review.md`: Qodo and Bugbot once at
open, at most two rounds, Copilot only on request. Do not bypass hooks, resolve
review threads without fixing/verifying them, or merge on incomplete reviews.
Release only when authorized, through the maintained punt-kit auto executor
and release playbook using `ethos-punt-release` or `/punt:auto release`.

## On-demand tools

Native `ethos-*` skills adapt maintained PR/FAQ, Z-spec, Quarry, collaboration,
release and setup procedures. Load only for the relevant task. Reuse user
decisions already given; ask only for missing inputs. MCPs are optional helpers,
not product prerequisites. Use installed CLI `--help` for fallback contracts;
report a missing prerequisite rather than inventing an API or silently skipping.
See [setup and verification](opencode-setup.md).

Preserve `.envrc`, `.envrc.local`, `.punt-labs/vox/` and existing records.
Never print secrets, resolved configuration, or credential-bearing environment
values. Do not add ignore rules to hide runtime files in source/record trees.
