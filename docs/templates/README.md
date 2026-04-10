# Mission Archetypes

A mission contract is a spectrum. The leader controls the agent's
latitude through contract specificity — not a mode switch, not a
separate field. The same agent, same personality, same expertise. The
contract determines how much room they have.

## The Spectrum

| Archetype | Write-set | Criteria | Rounds | Agency |
|-----------|-----------|----------|--------|--------|
| [Implement](implement.yaml) | Tight (specific files) | Specific, verifiable | 1-2 | Execute the spec |
| [Design](design.yaml) | Broad (directory) | Open-ended, judgment-based | 2-3 | Propose, evaluate, recommend |
| [Review](review.yaml) | Empty (read-only) | Findings with confidence | 1 | Assess and report |
| [Investigate](investigate.yaml) | Empty or docs/ | Questions answered | 1-2 | Research and synthesize |

## How Agency Works

Agency is not a field on the contract. It emerges from how the leader
writes the contract:

- **Tight write-set + specific criteria = low agency.** "Change this
  file. Make this test pass. Don't touch anything else." The agent
  executes.

- **Broad write-set + open criteria = high agency.** "Evaluate three
  approaches. Recommend one with tradeoffs. Write up your reasoning."
  The agent exercises judgment.

The agent's expertise and personality are always present. A Go
specialist brings Kernighan's design sensibility to both an
implementation task and a design task. The difference is whether the
contract says "implement this specific fix" or "figure out the best
approach."

## When to Use Each

**Implement** when you know what needs to happen and you want it
executed reliably. Most bug fixes, feature implementations with clear
specs, and refactoring tasks are implement missions.

**Design** when the problem is clear but the solution isn't. The
agent's domain expertise is the value — you hired them for their
judgment, not just their syntax. Architecture decisions, system
design, and "how should we approach X?" questions are design missions.

**Review** when you need an independent assessment. The reviewer reads
the code, reports findings with confidence ratings, and suggests
fixes — but never applies them. Keeps the reviewer honest: they
can't rationalize their own changes.

**Investigate** when you don't know enough to write a spec. The
investigator explores the question, synthesizes findings, and
recommends next steps. Debugging with unknown root cause, competitive
research, and "how does this actually work?" questions are
investigation missions.

## Mixing Archetypes

A multi-round mission can shift archetypes between rounds. Round 1
might be an investigation ("find the root cause"). After reflection,
round 2 becomes an implementation ("fix it in this file"). The
contract schema doesn't change — the leader writes different success
criteria for round 2 via the advance step.

## Real Example

The [live example walkthrough](../example/) uses an implement
archetype: bead `ethos-db7`, 1 file, 4 specific criteria, 1 round,
12 minutes 55 seconds from claim to merge. The contract said "one
ReadFile, same bytes, make check passes." The worker delivered
exactly that.

A design mission for the same problem domain might have said:
"evaluate three approaches to eliminate the TOCTOU in
checkVerifierHash. Consider: (a) thread bytes through existing API,
(b) export rejectSymlink and use Store.Load, (c) add flock around
the read chain. Recommend one with tradeoffs." Different contract,
same agent, same file, higher agency.
