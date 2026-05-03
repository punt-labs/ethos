# Use Case: Conversation and Mission Capture

How ethos turns Claude Code conversations and ethos missions into
durable markdown artifacts that ship in git alongside the code they
produced.

## Problem

A Claude Code session and an ethos mission are different shapes of the
same thing: a structured exchange that produces a code change. Each has
its own raw event log — `~/.claude/projects/<cwd>/<session-id>.jsonl`
for sessions, `~/.punt-labs/ethos/missions/<mission-id>.{yaml,jsonl}`
for missions — but those files live outside the repo. They are not
signed, not reviewed, and not durable across machine wipes.

Three concrete failures result:

1. **Reasoning evaporates.** A bug fix lands; nine months later nobody
   can answer "why this approach and not the other two?" The code says
   what; the reasoning that produced the what is on a laptop that was
   reimaged.
2. **Audit asks have no answers.** Compliance, post-mortem, or simple
   review questions about how a decision was reached cannot be
   recovered from the repo. They depend on a separate corpus that no
   tool curates.
3. **Quarry is rebuildable.** The semantic search index can — and
   periodically must — be torn down and reconstructed. Anything that
   only lived in quarry is gone. Git is the only system that survives
   reindex.

The reference Python primitives in `punt-labs/.bin/` proved the shape:
`jsonl-to-quarry.py` distills a 38 MB session JSONL down to 2.9 MB of
markdown (88-92% reduction); `scrub-pre-ingest.py` redacts secrets and
profanity. Both are well-tested, idempotent, and fast (~1.15 s end to
end on a real session). They produce the right artifact. They do not
yet ship with anything — they sit in a scripts directory and run only
when a human invokes them.

This use case binds those primitives into ethos so the artifact lands
in git automatically, on every commit, alongside the code change that
produced it.

## Personas

Two distinct flows, two pairs of personas.

### Conversation capture: human + lead agent

The primary user is a developer working with Claude Code. They hold a
free-form conversation — explore the code, make decisions, write the
fix, run the tests, commit. The lead agent is the other half of the
conversation. Sub-agents may spawn during the session but their work
folds into the parent transcript via the `Task` tool result blocks.

**Human developer.** Wants the reasoning behind their commits to
survive on the same timeline as the code. Does not want to remember to
run a separate command. Does not want capture to slow down their
commits beyond an audible blip. Will tolerate a per-commit cost in
exchange for "code and reasoning ship together."

**Lead agent (Claude in the primary session).** Treats the session
JSONL as an opaque side effect — does not read it, does not write
markdown to it. The capture system runs at the git layer below the
agent and is invisible to the agent's reasoning state.

### Mission capture: lead agent + sub-agents

A mission is a typed delegation: leader writes a contract via
`ethos mission create`, worker reads the contract via
`ethos mission show`, worker emits a result via
`ethos mission result`. The leader closes via `ethos mission close`
once the verdict is satisfactory. Each round of work happens in its
own sub-agent JSONL.

**Lead agent (mission leader).** Owns the contract and the
synthesis. Writes the contract, reflects between rounds, decides
pass/continue/escalate, runs `ethos mission close`. The leader's
reasoning lives in the primary session JSONL, not in the mission's
own files.

**Sub-agent (mission worker).** Reads the contract, does the work,
writes a result YAML. The worker's transcript is *the* primary record
of how the work was done — every Read, every Edit, every Bash, every
internal step is in the worker's session JSONL. The worker's
reasoning is the load-bearing input for mission capture.

**Frozen evaluator.** Reviews the worker's result against the contract.
The evaluator's reasoning also lives in its sub-agent JSONL.

The mission has *N* worker JSONLs (one per round, plus possibly per
evaluator round) plus the contract YAML, the result YAMLs, the
reflections, and the append-only event log. All five are the inputs
to mission capture.

## Use Cases

### UC-1: Conversation capture on every commit

A developer runs `git commit` after writing or fixing code. Before the
commit object is created, ethos captures the active session as
markdown, stages it, and the resulting commit contains both the code
change and the captured transcript.

**Scenario.** Sue is fixing a flaky test. She and the lead agent
discuss three possible root causes, decide on one, write the fix,
re-run the suite, and `git commit -m "fix: stabilize race in
TestParseRoster"`. The pre-commit hook fires, distills the active
session JSONL into `.ethos/sessions/<session-id>.md`, scrubs it for
secrets, stages the file, and the commit completes. The diff includes
both the test fix and the markdown. Six months later, when somebody
asks "why this fix and not invalidate-and-retry?", `git log -p` on
the test file links straight to the conversation that decided.

**Acceptance.** The session markdown is present at HEAD, scrubbed,
and contains the human prompts, agent text, tool inputs, and any
sub-agent reports for the conversation up to the moment of commit.

### UC-2: Mission capture on every commit during a mission's life

The leader spawns a worker via `ethos mission create` + `Agent`. The
worker does its work and commits. On the worker's commit, the mission
capture file is regenerated with the contract YAML, the event log so
far, and the worker's transcript. After the worker reports back, the
leader commits the close — and the mission file is regenerated again,
this time including the result YAML, reflections, and any subsequent
rounds.

**Scenario.** Brian (`bwk` worker) takes mission `m-2026-04-25-001`
to write this design doc. Across his work he makes three commits:
draft, review-fix, polish. On each commit the pre-commit hook
detects he's working under an open mission, regenerates
`.ethos/missions/m-2026-04-25-001.md`, and stages it. The leader
later closes the mission with `ethos mission close`; on the close
commit the leader's pre-commit hook regenerates the same file, now
with the result and reflection rolled in. The complete mission
record is in git, on the same commits as the code it produced.

**Acceptance.** The mission markdown is present at HEAD on every
commit produced inside an open mission and on the close commit. It
contains the contract, all rounds of worker transcripts, all
results, all reflections, and the append-only event log up to that
moment.

### UC-3: Backfill historical sessions and missions

A user who installs ethos capture for the first time wants their
existing JSONLs (sessions on `main`, closed missions in the global
store) to be captured retroactively. Runs `ethos capture backfill`.
The command walks the available JSONLs, distills and scrubs each,
and offers to commit them in batches.

**Scenario.** Capture ships in v3.6.0. Pat installs it and wants the
last quarter of work captured. They run `ethos capture backfill`.
The command finds 47 closed missions and ~120 session JSONLs whose
session ID appears in the local Claude Code projects directory.
Output: a list of missing capture files plus a recommended single
commit on a `chore/capture-backfill` branch. Pat reviews the diff,
opens a PR, merges. The corpus is now in git.

**Acceptance.** Re-running `ethos capture backfill` after the first
run is a no-op when nothing has changed. Re-running after an upgrade
to the distiller or scrubber replaces every file whose source JSONL
or schema version implies a different output, and only those.

### UC-4: A capture failure aborts the commit, with a useful message

A developer's `.ethos/sessions/` directory is somehow read-only (a
chmod accident, a filesystem quota issue, an ENOSPC). They `git commit`.
The pre-commit hook tries to write the capture file, fails, and
exits non-zero. The commit aborts with a clear message that names the
failed operation, the path involved, and the recovery step.

**Scenario.** Alex's home directory hits its inode quota mid-day. They
`git commit` and see:

```text
ethos capture: cannot write .ethos/sessions/abc123.md (no space left on device)
fix: free space, then retry. To skip capture for this commit only, set
ETHOS_CAPTURE=skip and commit again.
commit aborted.
```

Alex frees space and re-commits. The capture writes successfully.

**Acceptance.** Every capture failure mode the spike enumerated
(scrub error, write failure, malformed JSONL) produces a non-zero
exit, an actionable message on stderr, and an unmodified working
tree (no half-written capture file, no half-staged code). The user
knows immediately what failed and what to do.

### UC-5: Capture co-exists with other pre-commit hooks

The user's repo already has `.git/hooks/pre-commit` from another tool
(pre-commit, husky, lefthook, a hand-rolled script). Installing ethos
capture must not silently take over. The user must consent and ethos
must chain cleanly with the existing hook.

**Scenario.** Robin's repo has a `pre-commit` framework hook running
ruff and mypy. They run `ethos capture install`. Ethos detects the
existing hook, prints a diff of what it would change, and offers
three options: chain (ethos runs first, then existing hook),
replace (back up existing, install ethos as the only hook), or
abort. Robin picks chain. The next commit runs ethos capture
(succeeds), then ruff (succeeds), then mypy (succeeds), then lands.

**Acceptance.** `ethos capture install` never silently overwrites a
non-ethos pre-commit hook. The chained path runs both hooks in a
documented order and propagates the first non-zero exit. Subsequent
re-installs are idempotent.

### UC-6: A multi-repo mission writes its capture in one canonical location

Some missions span multiple repos. The mission lives in the leader's
repo, but the worker's commits land in the cross-repo target. The
capture file for the mission must have one canonical home so a reader
finds it predictably.

**Scenario.** A mission updates an API in `langlearn-types` and a
consumer in `langlearn`. The leader is in `ethos`. The worker
commits in both `langlearn-types` and `langlearn`. The mission
capture file lives in `ethos/.ethos/missions/m-<id>.md` because
`ethos` is the leader's repo. Each repo's commits link to the
mission ID via a trailer so a reader can find the canonical capture
from any of the three repos.

**Acceptance.** The mission's capture file lives in exactly one
repo. The capture process in the worker's commits is conversation
capture (UC-1), not mission capture — the worker's session is in
its own JSONL and is captured to that repo's `.ethos/sessions/`.
The two artifacts cross-reference by mission ID.

### UC-7: Privacy escape hatch for sensitive work

The user is debugging a credentials issue and the session may include
literal secrets even after scrubbing. They mark the session
`--private` so capture is suppressed.

**Scenario.** Casey is rotating a vendor's API key and there is a
non-trivial chance their session JSONL contains the rotated key in a
shape the scrubber does not yet recognize. They run
`ethos capture private` to mark this session as private. Subsequent
commits in this session do not produce capture files. The session's
JSONL remains on disk locally; it is not committed to git in any
form.

**Acceptance.** A session marked private produces no capture file
on any commit. The marking lives only on the user's local machine
and survives across sub-shells of the same session. Removing the
marker requires an explicit unmark.

## Relationship Between Conversation Capture and Mission Capture

Conversation capture and mission capture share the same primitive
shapes — distill JSONL, scrub the result, write to a known path,
stage, commit. They differ in what counts as the source.

| | Conversation capture | Mission capture |
|---|---------------------|-----------------|
| **Scope** | One Claude Code session | One ethos mission (1..N rounds) |
| **Source** | Single JSONL | Contract YAML + event log + N worker JSONLs |
| **Output path** | `.ethos/sessions/<session-id>.md` | `.ethos/missions/<mission-id>.md` |
| **Trigger** | Every commit while a session is active | Every commit while a mission is open, plus the close commit |
| **Lifecycle** | One session may produce many commits, many PRs | Same — 1:N mission to commits |
| **Grows by** | Appending records to the session JSONL between commits | Appending events, results, reflections, and round JSONLs |
| **Replacement** | File replaced on each commit; HEAD always holds the full session-so-far | Same |

A mission that runs inside an active session produces *both* a
session capture (the leader's own conversation, including the
`ethos mission` calls) and a mission capture (the worker's
transcript, the contract, the event log). The two are
complementary: the session capture records the leader's framing
and the worker's report-back; the mission capture records the
worker's transcript itself and the typed contract artifacts.

This is intentional. The leader's reasoning is not in the worker's
JSONL — it is in the leader's session JSONL. The worker's
transcript is not in the leader's JSONL — sub-agents do not
inherit ambient context. Both artifacts are needed to reconstruct
"why this code".

## Success Metrics

Measurable, with a target and a measurement source.

| Metric | Target | How measured |
|---|---|---|
| Capture coverage on commits during an active session | ≥ 99% of commits | `git log` count vs. capture-file count after one week of typical use |
| Capture coverage on mission-period commits | 100% of commits between `mission create` and `mission close` | `git log --grep "mission m-"` plus presence of `.ethos/missions/<id>.md` at HEAD |
| Per-commit added latency (warm cache) | < 1.5 s on the M2 reference | timed pre-commit hook wall on `git commit` of representative changes |
| Capture file size after distill+scrub | ≥ 85% reduction vs. raw JSONL | `wc -c` on input vs. output, averaged over 10 representative sessions |
| Failure mode visibility | 100% of capture failures abort the commit with a non-zero exit | failure-mode test matrix from the spike, re-run as integration tests |
| Backfill idempotence | Re-running with no upstream change writes 0 files | `git status` after a second `ethos capture backfill` invocation |
| Scrubber category coverage | 11 categories supported (the existing set) | unit test suite of category fixtures |
| Coexistence with non-ethos pre-commit hooks | 0 silent overwrites of existing hooks | `ethos capture install` never replaces a hook without explicit user consent (test) |

A metric is missing on purpose: "user-perceived utility of the
captured markdown." That requires real use over time and is not
something to gate v1 on. Coverage and correctness are.

## Non-Goals

What this design is not. These exist to bound scope and prevent
feature creep into the next sprint.

- **Not a full git workflow tool.** Ethos runs one specific git hook
  with one specific job. It does not manage commits, branches, or
  pushes. It does not amend or sign. It does not manage the
  developer's other pre-commit needs.
- **Not a quarry indexer.** Quarry's existing repo-content
  ingestion picks up the markdown by virtue of it landing in git.
  No code in this design talks to quarry.
- **Not encryption at rest.** Captured markdown is plaintext on
  disk. Repos using ethos capture inherit the same secrecy
  envelope as the repo itself. Encrypted capture is a future
  problem if/when one comes up.
- **Not a way to redact tool results selectively.** The scrubber
  operates on the rendered markdown via regex categories. There is
  no per-tool or per-call escape hatch that lets the user say "do
  not capture this Read." If that is needed, the user marks the
  whole session private (UC-7).
- **Not a real-time observer.** Capture happens at commit time, not
  continuously. There is no daemon, no watcher, no streaming
  pipeline. The session JSONL grows as the developer works; the
  hook reads the latest snapshot when the developer commits.
- **Not a cross-repo scheduler.** UC-6's multi-repo mission case is
  handled by *placement* — the mission file lives in the leader's
  repo. Conversation capture in the other repos is the regular
  per-session per-repo mechanism. There is no cross-repo
  coordinator.
- **Not a replacement for `mission export`.** The existing
  `ethos mission export` command writes a different artifact for a
  different audience. The capture file is a transcript-shaped
  document; the export is a contract+result shape. They coexist.
- **Not a CI/CD feature.** The hook runs on the developer's
  machine. CI runs against committed artifacts. If CI cannot find
  a capture file because the developer disabled the hook, that is
  visible in the diff (the file is missing); CI does not need to
  enforce.
- **Not a project-management tool.** Beads track work items. The
  capture is reasoning. They link by mission ID and bead ID; they
  do not subsume each other.

## References

- Reference primitives: `punt-labs/.bin/jsonl-to-quarry.py`,
  `punt-labs/.bin/scrub-pre-ingest.py` — proven shapes
- Spike data: `research/research-2026-04-25-hook-timing-spike.md`
- Prep doc: `research/research-2026-04-25-persistence-and-audit-prep.md`
- Companion design doc:
  `docs/design-conversation-mission-capture.md`
- Mission contract schema: `docs/mission-skill-design.md`,
  DES-031 in `DESIGN.md`
- Quarry integration: `docs/quarry-integration.md`,
  `docs/quarry-mission-tagging.md`
