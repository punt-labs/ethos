---
name: ethos-punt-release
description: Execute the maintained punt-kit auto release playbook only for an explicitly authorized release
---

# Punt auto-release adapter

Run `python3 scripts/opencode.py sources release` in the current target checkout.
Read BOTH returned files: canonical workspace sibling
punt-kit/plugin/skills/auto/SKILL.md and plugin/playbooks/release.yaml.
Missing either is a blocking prerequisite with its exact path. Do not substitute
an invented `punt auto` CLI or a similarly named playbook. These sources must
be the maintained workspace pair, not a copied giant plugin skill.

For an approved release, execute that executor's protocol and playbook, including
preconditions, background script steps, postconditions, bounded diagnosis and
independent verification. Use `punt --help` / `punt release --help` to confirm
the installed contract. All steps execute against the current target repository,
NEVER in punt-kit. The engine needs the clean canonical target checkout on main;
if invoked in a feature worktree, report the prerequisite and hand off rather
than silently releasing a different checkout.

Translate Claude Bash/Agent calls to native shell/subagent calls. The leader
launches an independent verifier; specialists deny nested delegation. Preserve
this repo's Qodo/Bugbot/two-round/Copilot-on-request policy and all gates; no
hook bypasses or unverified thread resolution. Do not repeat already supplied
version/scope questions. Report tested merge/tag/artifact evidence, not intent.

For discovery/health/smoke requests, READ ONLY: locate and read the two sources
and installed help; do not run the playbook, release CLI, preconditions with
side effects, version bumps, push, tag, install or publication.
